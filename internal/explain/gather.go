package explain

import (
	"sort"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secwatch"
	"github.com/ownbase/ownbase/internal/vulnscan"
)

// GatherInput is everything Gather needs to produce a BaseStatus.
// All fields are optional — Gather produces a best-effort status for whatever
// is provided. The zero value is safe and results in a minimal status.
type GatherInput struct {
	// Config is the parsed ownbase.yaml. When nil, Services will be empty.
	Config *schema.OwnbaseConfig

	// RunningContainers is a set of container names currently running, keyed
	// by the full container name "ownbase-<name>".
	// Populated from runtime.QueryCurrentState. When nil, Running defaults to
	// false (unknown) for every service.
	RunningContainers map[string]bool

	// BackupStatus is the most recent backup posture, loaded from the on-Base
	// backup status file (backup.LoadStatus). Zero value = not yet backed up.
	BackupStatus backup.Status

	// DriftEvents is the list of runtime/ files that differ from what the
	// compiler produced. From reconcile.DetectDrift. Nil = no drift.
	DriftEvents []reconcile.DriftEvent

	// AuditLogPath is the path to the on-Base JSON Lines audit log.
	// When empty, the audit summary will be empty.
	AuditLogPath string

	// AuditMaxRecords is how many recent audit records to include.
	// Defaults to DefaultAuditMaxRecords when zero.
	AuditMaxRecords int

	// Exposure is the result of the most recent secwatch network exposure
	// scan. Zero value (Available=false) means the scan has not run yet.
	Exposure secwatch.ExposureResult

	// Access is the result of the most recent secwatch SSH access probe.
	// Zero value (Available=false) means the probe has not run yet.
	Access secwatch.AccessResult

	// Vulns is the most recent vulnerability scan result. Zero value
	// (Available=false) means no scan has run yet.
	Vulns vulnscan.VulnStatus
}

// Gather assembles a BaseStatus from all available on-Base sources.
//
// It never returns an error — a missing or unreadable input produces a
// zero/empty value for that field. All sources are on-Base files or in-memory
// structs; Gather never reaches the network.
func Gather(in GatherInput) *BaseStatus {
	s := &BaseStatus{
		GeneratedAt:   time.Now().UTC(),
		SchemaVersion: StatusSchemaVersion,
	}

	if in.Config != nil {
		s.Services = gatherServices(in.Config, in.RunningContainers)
	}

	s.Security = gatherSecurity(in.BackupStatus, in.DriftEvents, in.Exposure, in.Access, in.Vulns)

	maxRecs := in.AuditMaxRecords
	if maxRecs <= 0 {
		maxRecs = DefaultAuditMaxRecords
	}
	if in.AuditLogPath != "" {
		s.Audit = gatherAudit(in.AuditLogPath, maxRecs)
	}

	return s
}

func gatherServices(cfg *schema.OwnbaseConfig, running map[string]bool) []ServiceStatus {
	result := make([]ServiceStatus, 0, len(cfg.Services))
	for name, decl := range cfg.Services {
		isRunning := running["ownbase-"+name]
		domains := decl.EffectiveDomains()
		svc := ServiceStatus{
			Name:     name,
			Running:  isRunning,
			Healthy:  isRunning,
			Domains:  domains,
			Port:     decl.Port,
			Requires: decl.Requires,
		}
		if len(domains) > 0 {
			svc.Domain = domains[0]
		}
		svc.Repo = decl.Repo
		svc.Ref = decl.Ref
		result = append(result, svc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

func gatherSecurity(bs backup.Status, driftEvents []reconcile.DriftEvent, exposure secwatch.ExposureResult, access secwatch.AccessResult, vulns vulnscan.VulnStatus) SecurityStatus {
	sec := SecurityStatus{
		BackupRestorable: bs.Restorable,
		Exposure:         exposure,
		Access:           access,
		Vulns:            vulns,
	}
	if !bs.LastBackup.IsZero() {
		t := bs.LastBackup
		sec.LastBackup = &t
	}
	if !bs.LastVerified.IsZero() {
		t := bs.LastVerified
		sec.LastVerified = &t
	}
	if len(driftEvents) > 0 {
		sec.DriftDetected = true
		sec.DriftCount = len(driftEvents)
		files := make([]string, len(driftEvents))
		for i, e := range driftEvents {
			files[i] = e.Filename
		}
		sort.Strings(files)
		sec.DriftFiles = files
	}
	return sec
}

func gatherAudit(path string, n int) AuditSummary {
	records, err := authz.ReadRecentRecords(path, n)
	if err != nil || len(records) == 0 {
		return AuditSummary{}
	}
	recent := make([]RecentAction, len(records))
	for i, r := range records {
		recent[i] = RecentAction{
			Time:    r.Time,
			Action:  r.Action,
			Target:  r.Target,
			Outcome: r.Outcome,
		}
	}
	return AuditSummary{
		RecentActions: recent,
		TotalSeen:     len(records),
	}
}
