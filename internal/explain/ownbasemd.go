package explain

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/ownbase/ownbase/internal/schema"
)

//go:embed ownbasemd.tmpl
var ownbaseMDTemplateText string

// ownbaseMDTmpl is the parsed template, initialized once at package load.
var ownbaseMDTmpl = template.Must(template.New("ownbase.md").Parse(ownbaseMDTemplateText))

// OwnbaseMDFilename is the canonical name of the generated file.
const OwnbaseMDFilename = "OWNBASE.md"

// ownbaseMDData is the complete data the OWNBASE.md template renders from.
// It is built by RenderOwnbaseMD and is not exported — callers use the render
// functions.
type ownbaseMDData struct {
	Services []serviceMDEntry
	Security securityMDData
}

// serviceMDEntry is one service row in the OWNBASE.md Services section.
type serviceMDEntry struct {
	Name     string
	Source   string
	Ref      string
	Mirror   string
	Domain   string
	Port     int
	Requires []string
	Running  bool
	Healthy  bool
}

// securityMDData is the security posture section of OWNBASE.md.
// Time values are pre-formatted as strings so the template stays logic-free.
type securityMDData struct {
	BackupRestorable bool
	LastVerifiedStr  string // formatted "2006-01-02" or ""
	DriftDetected    bool
	DriftCount       int

	// Network exposure fields (from secwatch.ExposureResult).
	ExposureAvailable bool
	FirewallActive    bool
	UnexpectedCount   int
	UnexpectedPorts   []int // sorted list of unexpected internet-reachable port numbers

	// SSH access monitor fields (from secwatch.AccessResult).
	AccessAvailable   bool
	Fail2banAvailable bool
	Fail2banActive    bool
	BannedCount       int
	FailedAttempts    int

	// Vulnerability scan fields (from vulnscan.VulnStatus).
	VulnsAvailable  bool
	TrivyScanError  bool   // trivy installed but host scan failed
	VulnsScannedStr string // formatted "2006-01-02" or ""
	HostCritical    int
	HostHigh        int
	HostMedium      int
	HostLow         int
	// TotalCritical/TotalHigh sum host + all successfully scanned images so the
	// top-level indicator covers the full attack surface, not just the host OS.
	TotalCritical int
	TotalHigh     int
	// FixableCritical/FixableHigh are the subset of host critical/high CVEs that
	// have a known fix available (FixedIn != ""). Shown as a hint in OWNBASE.md
	// so the operator knows whether `ownbasectl security fix` will help.
	FixableCritical int
	FixableHigh     int
	// TrivyScanPending is true when trivy is installed but no scan has
	// completed yet (ScannedAt is zero). Distinct from TrivyScanError so
	// OWNBASE.md shows "first scan pending" instead of "scan failed".
	TrivyScanPending bool
	// HasFailedImageScans is true when at least one image scan returned
	// ScanFailed=true. The template must not show a clean ✓ in this state —
	// the attack surface is partially unassessed.
	HasFailedImageScans bool
	FailedImageCount    int
	ImageVulnLines      []imageVulnMDLine // one line per scanned image
}

// imageVulnMDLine is one service row in the vulnerability scan summary.
type imageVulnMDLine struct {
	Service    string
	ScanFailed bool
	Critical   int
	High       int
	Medium     int
	Low        int
}

// RenderOwnbaseMD generates the full OWNBASE.md text from the parsed config
// and the most recently gathered status. cfg must not be nil; status may be
// nil (renders with empty status — all stopped, no backups, no drift).
func RenderOwnbaseMD(cfg *schema.OwnbaseConfig, status *BaseStatus) (string, error) {
	data := buildOwnbaseMDData(cfg, status)
	var b strings.Builder
	if err := ownbaseMDTmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render OWNBASE.md: %w", err)
	}
	return b.String(), nil
}

// WriteOwnbaseMD renders OWNBASE.md and writes it to the checkout root. The
// file is written atomically (write-then-rename) so the reconciler never sees
// a partial file.
func WriteOwnbaseMD(checkoutPath string, cfg *schema.OwnbaseConfig, status *BaseStatus) error {
	content, err := RenderOwnbaseMD(cfg, status)
	if err != nil {
		return err
	}
	dest := filepath.Join(checkoutPath, OwnbaseMDFilename)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write OWNBASE.md: %w", err)
	}
	return os.Rename(tmp, dest)
}

// buildOwnbaseMDData assembles the OWNBASE.md template data.
func buildOwnbaseMDData(cfg *schema.OwnbaseConfig, status *BaseStatus) ownbaseMDData {
	running := make(map[string]bool)
	if status != nil {
		for _, svc := range status.Services {
			running[svc.Name] = svc.Running
		}
	}

	entries := make([]serviceMDEntry, 0, len(cfg.Services))
	for name, decl := range cfg.Services {
		e := serviceMDEntry{
			Name:     name,
			Domain:   decl.Domain,
			Port:     decl.Port,
			Requires: decl.Requires,
			Running:  running[name],
			Healthy:  running[name],
		}
		if decl.Source != "" {
			e.Source = decl.Source
			e.Ref = decl.Ref
		} else if decl.Mirror != "" {
			e.Mirror = decl.Mirror
			e.Ref = decl.Ref
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	var sec securityMDData
	if status != nil {
		sec.BackupRestorable = status.Security.BackupRestorable
		if !status.Security.LastVerified.IsZero() {
			sec.LastVerifiedStr = status.Security.LastVerified.Format("2006-01-02")
		}
		sec.DriftDetected = status.Security.DriftDetected
		sec.DriftCount = status.Security.DriftCount

		exp := status.Security.Exposure
		sec.ExposureAvailable = exp.Available
		sec.FirewallActive = exp.FirewallActive
		sec.UnexpectedCount = exp.UnexpectedCount
		for _, l := range exp.Listeners {
			if l.InternetReachable && !l.Expected {
				sec.UnexpectedPorts = append(sec.UnexpectedPorts, l.Port)
			}
		}
		sort.Ints(sec.UnexpectedPorts)

		acc := status.Security.Access
		sec.AccessAvailable = acc.Available
		sec.Fail2banAvailable = acc.Fail2banAvailable
		sec.Fail2banActive = acc.Fail2banActive
		sec.BannedCount = len(acc.BannedIPs)
		sec.FailedAttempts = acc.FailedAttempts

		v := status.Security.Vulns
		sec.VulnsAvailable = v.Available
		sec.TrivyScanError = v.TrivyInstalled && !v.Available && !v.ScannedAt.IsZero()
		sec.TrivyScanPending = v.TrivyInstalled && !v.Available && v.ScannedAt.IsZero()
		if !v.ScannedAt.IsZero() {
			sec.VulnsScannedStr = v.ScannedAt.Format("2006-01-02")
		}
		sec.HostCritical = v.Host.Critical
		sec.HostHigh = v.Host.High
		sec.HostMedium = v.Host.Medium
		sec.HostLow = v.Host.Low
		sec.FixableCritical = v.Host.FixableCritical
		sec.FixableHigh = v.Host.FixableHigh
		sec.TotalCritical = v.Host.Critical
		sec.TotalHigh = v.Host.High
		for _, img := range v.Images {
			line := imageVulnMDLine{
				Service:    img.Service,
				ScanFailed: img.ScanFailed,
			}
			if img.ScanFailed {
				sec.HasFailedImageScans = true
				sec.FailedImageCount++
			} else {
				line.Critical = img.Summary.Critical
				line.High = img.Summary.High
				line.Medium = img.Summary.Medium
				line.Low = img.Summary.Low
				sec.TotalCritical += img.Summary.Critical
				sec.TotalHigh += img.Summary.High
			}
			sec.ImageVulnLines = append(sec.ImageVulnLines, line)
		}
		sort.Slice(sec.ImageVulnLines, func(i, j int) bool {
			return sec.ImageVulnLines[i].Service < sec.ImageVulnLines[j].Service
		})
	}

	return ownbaseMDData{Services: entries, Security: sec}
}
