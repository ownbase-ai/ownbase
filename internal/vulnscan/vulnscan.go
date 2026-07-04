// Package vulnscan runs trivy to detect known CVEs in the host OS packages
// and running container images.
//
// Three scan functions:
//
//	GatherHostVulns     — scans installed OS packages for CVEs using trivy fs.
//	GatherImageVulns    — scans a list of ContainerTargets (running containers).
//	GatherVulns         — combines both into a single VulnStatus.
//	RunningContainers   — discovers running Podman containers via podman ps.
//
// All functions degrade gracefully: when trivy is not installed,
// Available=false is returned so Tier-1 tests pass off-VM.
//
// Honesty caveat: the scan answers "what does trivy find in installed packages
// and local images?" It cannot see vulnerabilities in code that does not match
// any package manifest, or CVEs not yet in the trivy database.
package vulnscan

import "time"

// MaxTopFindings is the maximum number of top findings included per target.
// Only CRITICAL and HIGH findings are included in Top.
const MaxTopFindings = 20

// VulnFinding is one CVE entry from a trivy scan.
type VulnFinding struct {
	VulnID   string `json:"vuln_id"`
	Package  string `json:"package"`
	Version  string `json:"version,omitempty"`
	FixedIn  string `json:"fixed_in,omitempty"`
	Severity string `json:"severity"`
	Title    string `json:"title,omitempty"`
}

// VulnSummary counts CVEs by severity for one scan target and holds the most
// severe findings for display.
type VulnSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`

	// FixableCritical and FixableHigh count CRITICAL/HIGH CVEs that have a
	// known fix available in the package repository (FixedIn != ""). The
	// remainder (Critical - FixableCritical, etc.) are "no fix yet" — known
	// vulnerabilities with no upstream or distro patch published.
	FixableCritical int `json:"fixable_critical,omitempty"`
	FixableHigh     int `json:"fixable_high,omitempty"`

	// Top holds the highest-severity findings (CRITICAL before HIGH), up to
	// MaxTopFindings. MEDIUM and LOW findings are counted but not stored.
	Top []VulnFinding `json:"top,omitempty"`
}

// Total returns the total number of CVEs across all severities.
func (s VulnSummary) Total() int {
	return s.Critical + s.High + s.Medium + s.Low
}

// ContainerTarget is a running container whose image should be scanned.
// Produced by RunningContainers and passed to GatherVulns / GatherImageVulns.
type ContainerTarget struct {
	// Service is the container name used in status and CLI output
	// (e.g. "ownbase-core-forgejo").
	Service string
	// Image is the full image reference as reported by podman
	// (e.g. "codeberg.org/forgejo/forgejo:9.0").
	Image string
}

// ImageVulns is the vulnerability result for one container image.
type ImageVulns struct {
	Service string      `json:"service"`
	Image   string      `json:"image"`
	Summary VulnSummary `json:"summary"`

	// ScanFailed is true when the image was found in local storage but trivy
	// could not scan it. Distinct from "image not built" (which is omitted
	// from results entirely). A failed scan must not be treated as clean.
	ScanFailed bool `json:"scan_failed,omitempty"`
}

// VulnStatus is the combined vulnerability scan result for a Base.
type VulnStatus struct {
	// Available is true when the scan ran to completion and the host result
	// is trustworthy. False means either trivy is absent or the host scan
	// command failed — both are "unknown", not "clean".
	Available bool `json:"available"`

	// TrivyInstalled is true when the trivy binary was found on PATH,
	// regardless of whether the scan succeeded. Allows callers to distinguish
	// "need to install trivy" (false) from "trivy installed but scan failed"
	// (true with Available=false).
	TrivyInstalled bool `json:"trivy_installed"`

	// ScannedAt is when GatherVulns was called. Set even when Available=false
	// so that concurrent scan goroutines can discard results from an older run.
	ScannedAt time.Time `json:"scanned_at,omitempty"`

	// Host holds the OS package scan result.
	Host VulnSummary `json:"host"`

	// Images holds per-service image scan results. Empty until the first
	// scan tick or when no services are running.
	Images []ImageVulns `json:"images,omitempty"`
}
