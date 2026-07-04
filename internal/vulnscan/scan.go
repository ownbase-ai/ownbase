package vulnscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// trivyReport is the top-level JSON schema trivy v2 emits.
type trivyReport struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Vulnerabilities []trivyVuln `json:"Vulnerabilities"`
}

type trivyVuln struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
}

// TrivyAvailable returns true when trivy is on PATH.
// Exported for the install package (ensureTrivy idempotency check) and tests.
func TrivyAvailable() bool {
	_, err := exec.LookPath("trivy")
	return err == nil
}

// GatherVulns runs both host OS and container image scans and returns the
// combined VulnStatus.
//
// targets is the list of running containers to scan (from RunningContainers).
// Pass nil to skip image scanning (e.g. when podman is unavailable).
//
// ScannedAt is always set (even on failure) so that concurrent goroutines can
// discard results from an older run that finished after a newer one completed.
func GatherVulns(ctx context.Context, targets []ContainerTarget) VulnStatus {
	now := time.Now().UTC()

	if !TrivyAvailable() {
		return VulnStatus{TrivyInstalled: false, ScannedAt: now}
	}

	host, hostOK := GatherHostVulns(ctx)

	// Always run image scans even if the host scan failed — trivy and podman
	// may be perfectly functional for container images, and skipping them
	// silently would leave CVEs in running services unreported.
	images := GatherImageVulns(ctx, targets)

	return VulnStatus{
		// Available reflects only the host scan: false means host data is
		// incomplete, but Images may still carry valid per-service results.
		Available:      hostOK,
		TrivyInstalled: true,
		ScannedAt:      now,
		Host:           host,
		Images:         images,
	}
}

// GatherHostVulns scans the host OS packages for known CVEs using trivy fs.
// Returns (summary, true) on success; (VulnSummary{}, false) when the scan
// command fails. The caller must treat false as "data unavailable" — not as a
// clean host — so a failed scan is never silently presented as zero CVEs.
func GatherHostVulns(ctx context.Context) (VulnSummary, bool) {
	out, err := runTrivy(ctx, "fs",
		"--quiet",
		"--format", "json",
		"--scanners", "vuln",
		"--pkg-types", "os",
		"/",
	)
	if err != nil {
		return VulnSummary{}, false
	}
	return ParseTrivyOutput(out), true
}

// GatherImageVulns scans each container in targets using trivy image.
//
// targets comes from RunningContainers — the images are guaranteed to be
// present in local Podman storage (a running container's image is always
// available). A trivy failure on a running image is a real scan error and
// is recorded with ScanFailed=true rather than silently omitted.
func GatherImageVulns(ctx context.Context, targets []ContainerTarget) []ImageVulns {
	if len(targets) == 0 {
		return nil
	}
	var results []ImageVulns
	for _, t := range targets {
		out, err := runTrivy(ctx, "image",
			"--quiet",
			"--format", "json",
			"--scanners", "vuln",
			t.Image,
		)
		if err != nil {
			results = append(results, ImageVulns{
				Service:    t.Service,
				Image:      t.Image,
				ScanFailed: true,
			})
			continue
		}
		results = append(results, ImageVulns{
			Service: t.Service,
			Image:   t.Image,
			Summary: ParseTrivyOutput(out),
		})
	}
	return results
}

// RunningContainers returns a deduplicated list of images from all currently
// running rootful Podman containers. Called by the daemon before each vuln
// scan so core services (Forgejo, Caddy) and user-defined services are all
// covered — not just services declared in ownbase.yaml.
//
// Returns nil when podman is absent or no containers are running.
func RunningContainers(ctx context.Context) []ContainerTarget {
	if _, err := exec.LookPath("podman"); err != nil {
		return nil
	}

	out, err := exec.CommandContext(ctx, "podman", "ps", "--format", "json").Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	// Podman emits Names as a JSON array in v5+ and as a plain string in older
	// releases. Use json.RawMessage so we can handle both shapes gracefully.
	var containers []struct {
		Names json.RawMessage `json:"Names"`
		Image string          `json:"Image"`
	}
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil
	}

	// Deduplicate by image reference — multiple containers can share an image
	// (e.g. a sidecar and main container from the same build). Scan each image
	// once and label it with the first container's name.
	seen := make(map[string]bool)
	var targets []ContainerTarget
	for _, c := range containers {
		if c.Image == "" || seen[c.Image] {
			continue
		}
		seen[c.Image] = true

		// Resolve the container name, tolerating both []string and string JSON.
		name := ""
		var nameList []string
		if json.Unmarshal(c.Names, &nameList) == nil && len(nameList) > 0 {
			name = nameList[0]
		} else {
			var singleName string
			if json.Unmarshal(c.Names, &singleName) == nil {
				name = singleName
			}
		}
		targets = append(targets, ContainerTarget{Service: name, Image: c.Image})
	}
	return targets
}

// ParseTrivyOutput parses trivy JSON output into a VulnSummary.
// Exported for Tier-1 fixture-string testing.
func ParseTrivyOutput(output string) VulnSummary {
	var report trivyReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		return VulnSummary{}
	}

	var summary VulnSummary
	var top []VulnFinding

	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			sev := strings.ToUpper(v.Severity)
			f := VulnFinding{
				VulnID:   v.VulnerabilityID,
				Package:  v.PkgName,
				Version:  v.InstalledVersion,
				FixedIn:  v.FixedVersion,
				Severity: sev,
				Title:    v.Title,
			}
			switch sev {
			case "CRITICAL":
				summary.Critical++
				if f.FixedIn != "" {
					summary.FixableCritical++
				}
				top = append(top, f)
			case "HIGH":
				summary.High++
				if f.FixedIn != "" {
					summary.FixableHigh++
				}
				top = append(top, f)
			case "MEDIUM":
				summary.Medium++
			case "LOW":
				summary.Low++
			}
		}
	}

	// Sort: CRITICAL before HIGH; stable so findings at the same severity
	// retain their original order from the trivy output.
	sort.SliceStable(top, func(i, j int) bool {
		return severityRank(top[i].Severity) > severityRank(top[j].Severity)
	})
	if len(top) > MaxTopFindings {
		top = top[:MaxTopFindings]
	}
	summary.Top = top

	return summary
}

// severityRank maps a severity string to a sort key (higher = more severe).
func severityRank(sev string) int {
	switch sev {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	default:
		return 0
	}
}

// runTrivy executes trivy with the given subcommand and args and returns the
// stdout output. Uses a 10-minute per-invocation timeout to accommodate the
// initial vulnerability database download (~100 MB on first run).
//
// When trivy exits non-zero but still produced stdout (e.g. it emitted
// warnings or policy messages alongside valid JSON), the stdout is returned
// without an error so callers can still parse the findings. Only a genuine
// failure with no usable output returns an error.
func runTrivy(ctx context.Context, subcommand string, args ...string) (string, error) {
	scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	allArgs := append([]string{subcommand}, args...)
	out, err := exec.CommandContext(scanCtx, "trivy", allArgs...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(out) > 0 && json.Valid(out) {
			// Non-zero exit with valid JSON stdout — trivy emitted scan results
			// alongside a warning or policy signal. Return the JSON so callers
			// can still parse the findings. Only suppress the error when stdout
			// is well-formed JSON; a plain-text error message must not be
			// silently treated as a clean (zero-CVE) scan.
			return string(out), nil
		}
		return "", fmt.Errorf("trivy %s: %w", subcommand, err)
	}
	return string(out), nil
}
