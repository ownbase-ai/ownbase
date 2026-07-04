package vulnscan_test

import (
	"testing"

	"github.com/ownbase/ownbase/internal/vulnscan"
)

// trivyFixture is a minimal trivy v2 JSON output with one finding of each severity.
const trivyFixture = `{
  "SchemaVersion": 2,
  "ArtifactName": "/",
  "Results": [
    {
      "Target": "ubuntu 24.04 (ubuntu)",
      "Class": "os-pkgs",
      "Type": "ubuntu",
      "Vulnerabilities": [
        {
          "VulnerabilityID": "CVE-2024-0001",
          "PkgName": "libssl3",
          "InstalledVersion": "3.0.2-0ubuntu1.10",
          "FixedVersion": "3.0.2-0ubuntu1.12",
          "Severity": "CRITICAL",
          "Title": "OpenSSL critical issue"
        },
        {
          "VulnerabilityID": "CVE-2024-0002",
          "PkgName": "curl",
          "InstalledVersion": "7.81.0-1ubuntu1.10",
          "FixedVersion": "",
          "Severity": "HIGH",
          "Title": "curl high severity issue"
        },
        {
          "VulnerabilityID": "CVE-2024-0003",
          "PkgName": "bash",
          "InstalledVersion": "5.1-6ubuntu1",
          "FixedVersion": "5.1-6ubuntu1.1",
          "Severity": "MEDIUM",
          "Title": "bash medium issue"
        },
        {
          "VulnerabilityID": "CVE-2024-0004",
          "PkgName": "grep",
          "InstalledVersion": "3.7-1",
          "FixedVersion": "",
          "Severity": "LOW",
          "Title": "grep low issue"
        }
      ]
    }
  ]
}`

// trivyMultiCritical has three CRITICAL and one HIGH to verify Top ordering.
const trivyMultiCritical = `{
  "SchemaVersion": 2,
  "Results": [
    {
      "Target": "ubuntu",
      "Vulnerabilities": [
        {"VulnerabilityID":"CVE-2024-A","PkgName":"pkgA","Severity":"HIGH"},
        {"VulnerabilityID":"CVE-2024-B","PkgName":"pkgB","Severity":"CRITICAL"},
        {"VulnerabilityID":"CVE-2024-C","PkgName":"pkgC","Severity":"CRITICAL"},
        {"VulnerabilityID":"CVE-2024-D","PkgName":"pkgD","Severity":"CRITICAL"}
      ]
    }
  ]
}`

// ---------------------------------------------------------------------------
// ParseTrivyOutput — counts
// ---------------------------------------------------------------------------

func TestParseTrivyOutput_Counts(t *testing.T) {
	s := vulnscan.ParseTrivyOutput(trivyFixture)

	if s.Critical != 1 {
		t.Errorf("Critical = %d, want 1", s.Critical)
	}
	if s.High != 1 {
		t.Errorf("High = %d, want 1", s.High)
	}
	if s.Medium != 1 {
		t.Errorf("Medium = %d, want 1", s.Medium)
	}
	if s.Low != 1 {
		t.Errorf("Low = %d, want 1", s.Low)
	}
	if s.Total() != 4 {
		t.Errorf("Total = %d, want 4", s.Total())
	}
}

// ---------------------------------------------------------------------------
// ParseTrivyOutput — Top findings
// ---------------------------------------------------------------------------

func TestParseTrivyOutput_TopFindings_OnlyCriticalAndHigh(t *testing.T) {
	s := vulnscan.ParseTrivyOutput(trivyFixture)

	// Top must contain only CRITICAL and HIGH, not MEDIUM or LOW.
	if len(s.Top) != 2 {
		t.Fatalf("Top = %d findings, want 2 (CRITICAL + HIGH only)", len(s.Top))
	}
	// CRITICAL must sort before HIGH.
	if s.Top[0].Severity != "CRITICAL" {
		t.Errorf("Top[0].Severity = %q, want CRITICAL", s.Top[0].Severity)
	}
	if s.Top[1].Severity != "HIGH" {
		t.Errorf("Top[1].Severity = %q, want HIGH", s.Top[1].Severity)
	}
}

func TestParseTrivyOutput_TopFindings_Fields(t *testing.T) {
	s := vulnscan.ParseTrivyOutput(trivyFixture)
	if len(s.Top) == 0 {
		t.Fatal("expected at least one top finding")
	}
	f := s.Top[0]
	if f.VulnID != "CVE-2024-0001" {
		t.Errorf("VulnID = %q, want CVE-2024-0001", f.VulnID)
	}
	if f.Package != "libssl3" {
		t.Errorf("Package = %q, want libssl3", f.Package)
	}
	if f.Version != "3.0.2-0ubuntu1.10" {
		t.Errorf("Version = %q", f.Version)
	}
	if f.FixedIn != "3.0.2-0ubuntu1.12" {
		t.Errorf("FixedIn = %q", f.FixedIn)
	}
	if f.Title != "OpenSSL critical issue" {
		t.Errorf("Title = %q", f.Title)
	}
}

func TestParseTrivyOutput_TopFindings_CriticalBeforeHigh(t *testing.T) {
	// trivyMultiCritical has HIGH first in JSON — verify sort puts CRITICALs first.
	s := vulnscan.ParseTrivyOutput(trivyMultiCritical)
	if s.Critical != 3 {
		t.Errorf("Critical = %d, want 3", s.Critical)
	}
	if s.High != 1 {
		t.Errorf("High = %d, want 1", s.High)
	}
	// All four should appear in Top.
	if len(s.Top) != 4 {
		t.Fatalf("Top = %d, want 4", len(s.Top))
	}
	// First three must be CRITICAL.
	for i := 0; i < 3; i++ {
		if s.Top[i].Severity != "CRITICAL" {
			t.Errorf("Top[%d].Severity = %q, want CRITICAL", i, s.Top[i].Severity)
		}
	}
	if s.Top[3].Severity != "HIGH" {
		t.Errorf("Top[3].Severity = %q, want HIGH", s.Top[3].Severity)
	}
}

// ---------------------------------------------------------------------------
// ParseTrivyOutput — edge cases
// ---------------------------------------------------------------------------

func TestParseTrivyOutput_EmptyInput(t *testing.T) {
	s := vulnscan.ParseTrivyOutput("")
	if s.Total() != 0 {
		t.Errorf("empty input should produce zero counts, got %+v", s)
	}
	if len(s.Top) != 0 {
		t.Errorf("empty input should have no Top findings, got %v", s.Top)
	}
}

func TestParseTrivyOutput_NullVulnerabilities(t *testing.T) {
	noVulns := `{"SchemaVersion":2,"Results":[{"Target":"ubuntu","Vulnerabilities":null}]}`
	s := vulnscan.ParseTrivyOutput(noVulns)
	if s.Total() != 0 {
		t.Errorf("null Vulnerabilities should produce zero counts, got %+v", s)
	}
}

func TestParseTrivyOutput_UnknownSeverity(t *testing.T) {
	// Unknown severities should not increment any counter or appear in Top.
	unknown := `{"SchemaVersion":2,"Results":[{"Target":"t","Vulnerabilities":[
		{"VulnerabilityID":"CVE-X","PkgName":"foo","Severity":"UNKNOWN"}
	]}]}`
	s := vulnscan.ParseTrivyOutput(unknown)
	if s.Total() != 0 {
		t.Errorf("UNKNOWN severity should not count, got %+v", s)
	}
	if len(s.Top) != 0 {
		t.Errorf("UNKNOWN severity should not appear in Top, got %v", s.Top)
	}
}

// ---------------------------------------------------------------------------
// VulnSummary.Total
// ---------------------------------------------------------------------------

func TestVulnSummary_Total(t *testing.T) {
	s := vulnscan.VulnSummary{Critical: 2, High: 5, Medium: 10, Low: 3}
	if s.Total() != 20 {
		t.Errorf("Total = %d, want 20", s.Total())
	}
}

// ---------------------------------------------------------------------------
// VulnStatus zero value
// ---------------------------------------------------------------------------

func TestVulnStatus_ZeroValue(t *testing.T) {
	var vs vulnscan.VulnStatus
	if vs.Available {
		t.Error("zero VulnStatus should have Available=false")
	}
	if vs.TrivyInstalled {
		t.Error("zero VulnStatus should have TrivyInstalled=false")
	}
	if vs.Host.Total() != 0 {
		t.Errorf("zero VulnStatus.Host should have zero counts, got %+v", vs.Host)
	}
	if len(vs.Images) != 0 {
		t.Errorf("zero VulnStatus.Images should be empty, got %v", vs.Images)
	}
	if !vs.ScannedAt.IsZero() {
		t.Error("zero VulnStatus.ScannedAt should be zero")
	}
}

func TestImageVulns_ScanFailed_ZeroValue(t *testing.T) {
	var iv vulnscan.ImageVulns
	if iv.ScanFailed {
		t.Error("zero ImageVulns should have ScanFailed=false")
	}
	if iv.Summary.Total() != 0 {
		t.Errorf("zero ImageVulns.Summary should have zero counts, got %+v", iv.Summary)
	}
}
