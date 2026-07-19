// Package explain generates the machine-readable status JSON from live Base
// state and serves the status API that ownbasectl and any HTTP client consume.
//
//	config + live status (M3) + restorable (M6) + audit (M3)
//	        │
//	        └─> status API (JSON) ─> ownbasectl status/checkup, any HTTP client
package explain

import (
	"time"

	"github.com/ownbase/ownbase/internal/secwatch"
	"github.com/ownbase/ownbase/internal/vulnscan"
)

// StatusSchemaVersion is the version of the BaseStatus JSON contract.
// Increment when fields are removed or renamed.
const StatusSchemaVersion = "v3"

// DefaultAuditMaxRecords is how many recent audit records the status API
// surfaces by default.
const DefaultAuditMaxRecords = 20

// BaseStatus is the structured health and status of one Base.
//
// This is the durable JSON contract between the agent and any briefing
// consumer (opterm). SchemaVersion guards against silent breaking changes.
// Field names must not change without bumping SchemaVersion.
type BaseStatus struct {
	GeneratedAt   time.Time       `json:"generated_at"`
	SchemaVersion string          `json:"schema_version"`
	Services      []ServiceStatus `json:"services"`
	Security      SecurityStatus  `json:"security"`
	Updates       UpdateStatus    `json:"updates"`
	Audit         AuditSummary    `json:"audit"`
}

// ServiceStatus is the health and source state of one service.
type ServiceStatus struct {
	Name string `json:"name"`

	// Running is true when the container is up.
	Running bool `json:"running"`
	// Healthy is true when Running and the last health probe passed.
	// V1: same as Running; future: driven by the health probe result.
	Healthy bool `json:"healthy"`
	// HealthProbeResult is the last health probe outcome, if any.
	// Empty means no probe has run yet. (M12 Tier-2 seam.)
	HealthProbeResult string `json:"health_probe_result,omitempty"`

	// Repo is the external git URL (repo:) the service builds from.
	Repo string `json:"repo,omitempty"`
	Ref  string `json:"ref,omitempty"` // pinned branch/tag/SHA; empty = default HEAD

	// Public exposure. Domain is the first entry of Domains, kept for
	// backward-compatible API consumers that only read a single hostname;
	// Domains is the full effective list (see schema.ServiceDecl.EffectiveDomains).
	Domain  string   `json:"domain,omitempty"`
	Domains []string `json:"domains,omitempty"`
	Port    int      `json:"port,omitempty"`

	// Requires lists capability names this service depends on.
	Requires []string `json:"requires,omitempty"`
}

// SecurityStatus is the security posture of the Base in plain outcome language.
// Field names must never expose product names, registry hosts, or raw machinery
// (Brand & Positioning — M8 exit criterion).
type SecurityStatus struct {
	// BackupRestorable is true only after a verified restore drill has passed.
	// A backup that has never been verified is NOT restorable by definition.
	BackupRestorable bool `json:"backup_restorable"`
	// LastVerified is when the most recent verified restore drill passed.
	// Pointer so omitempty suppresses it on a Base that has never verified
	// (encoding/json's omitempty does not suppress a zero time.Time value).
	LastVerified *time.Time `json:"last_verified,omitempty"`
	// LastBackup is when the most recent backup snapshot was taken.
	// Pointer so omitempty suppresses it on a Base that has never backed up.
	LastBackup *time.Time `json:"last_backup,omitempty"`

	// DriftDetected is true when runtime/ differs from what the compiler produced.
	// Any drift is a tamper signal — runtime/ has exactly one writer (the agent).
	DriftDetected bool `json:"drift_detected"`
	// DriftCount is the number of files that differ.
	DriftCount int `json:"drift_count,omitempty"`
	// DriftFiles is the sorted list of drifted file basenames.
	DriftFiles []string `json:"drift_files,omitempty"`

	// CertExpiryDays is the number of days until the TLS certificate expires.
	// Zero means unknown or not yet populated (M12 Tier-2 seam).
	CertExpiryDays int `json:"cert_expiry_days,omitempty"`

	// DiskUsedPercent is the percentage of disk space used on the primary
	// data partition (0–100). Zero means unknown. (M12 Tier-2 seam.)
	DiskUsedPercent int `json:"disk_used_percent,omitempty"`

	// Exposure is the result of the network exposure inventory. Populated
	// after every reconcile by the secwatch package.
	Exposure secwatch.ExposureResult `json:"exposure"`

	// Access is the SSH access monitor result. Populated after every
	// reconcile by the secwatch package.
	Access secwatch.AccessResult `json:"access"`

	// Vulns is the vulnerability scan result. Populated by the daily
	// vulnscan tick; zero value (Available=false) means no scan has run yet.
	Vulns vulnscan.VulnStatus `json:"vulns"`
}

// UpdateStatus summarises how far behind each service is from its source.
// Populated by the agent's update-interval tick; zero value (empty Drift)
// means detection has not run yet.
type UpdateStatus struct {
	// Drift is the per-service drift state. Empty until the first tick.
	Drift []ServiceDrift `json:"drift,omitempty"`
}

// ServiceDrift holds the drift state for one service.
type ServiceDrift struct {
	// Service is the service key in ownbase.yaml.
	Service string `json:"service"`
	// Ref is the currently pinned ref (branch, tag, or SHA).
	Ref string `json:"ref"`
	// Branch is the default branch of the source repo (e.g. "main").
	Branch string `json:"branch,omitempty"`
	// CommitsBehind is how many commits the pinned ref is behind the default
	// branch HEAD. Zero means up to date on the branch dimension.
	CommitsBehind int `json:"commits_behind"`
	// NewestTag is the highest semver tag available in the local bare repo.
	// Empty when the repo has no tags.
	NewestTag string `json:"newest_tag,omitempty"`
	// UpToDate is true when CommitsBehind == 0 and there is no newer tag.
	UpToDate bool `json:"up_to_date"`
}

// AuditSummary is a snapshot of recent governed actions from the on-Base
// audit log.
type AuditSummary struct {
	// RecentActions is the most recent DefaultAuditMaxRecords audit records.
	RecentActions []RecentAction `json:"recent_actions,omitempty"`
	// TotalSeen is the number of records that were read and included.
	TotalSeen int `json:"total_seen"`
}

// RecentAction is one audit log entry in briefing-friendly form.
type RecentAction struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Outcome string    `json:"outcome"`
}
