package authz

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultAuditLogPath is the canonical on-Base location for the audit log.
// The file is owned by the agent process; all records are append-only.
const DefaultAuditLogPath = "/opt/ownbase/logs/audit.log"

// AuditOutcome values for AuditRecord.Outcome.
const (
	OutcomeApplied       = "applied"
	OutcomeRolledBack    = "rolled_back"
	OutcomeRefused       = "refused"
	OutcomeError         = "error"
	OutcomeRollbackError = "rollback_error"
)

// AuditRecord is one entry in the audit log. Serialized as a JSON object,
// one record per line (JSON Lines / NDJSON). The format is stable and
// exportable with standard tools (jq, grep, etc.).
type AuditRecord struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Tier    string    `json:"tier"`
	Outcome string    `json:"outcome"`
	Error   string    `json:"error,omitempty"`
}

// AuditLogger records every agent action and its outcome. The interface exists
// so callers receive either the real on-Base file log or a no-op, without
// knowing which.
type AuditLogger interface {
	// Record appends one entry. outcome must be one of the Outcome* constants.
	// errMsg is the error string if the action failed, empty otherwise.
	Record(action schema.Action, outcome, errMsg string) error
}

// AuditLog is the production AuditLogger: an append-only JSON Lines file on
// the Base. The log is user-owned and human/machine readable.
//
// AuditLog is safe for concurrent use.
type AuditLog struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// NewAuditLog opens (or creates) the audit log at path. The parent directory
// is created with mode 0750 if it does not exist.
func NewAuditLog(path string) (*AuditLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("audit log: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit log: open %s: %w", path, err)
	}
	return &AuditLog{path: path, f: f}, nil
}

// Record appends one audit entry to the file.
func (l *AuditLog) Record(action schema.Action, outcome, errMsg string) error {
	r := AuditRecord{
		Time:    time.Now().UTC(),
		Action:  string(action.Type),
		Target:  action.Target,
		Tier:    string(action.DefaultTier),
		Outcome: outcome,
		Error:   errMsg,
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("audit log: marshal: %w", err)
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(data); err != nil {
		return fmt.Errorf("audit log: write: %w", err)
	}
	return nil
}

// Close flushes and closes the underlying file.
func (l *AuditLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Path returns the file path the log was opened at.
func (l *AuditLog) Path() string { return l.path }

// nopAuditLog silently discards all records. Used for dry-run and tests that
// do not care about the log.
type nopAuditLog struct{}

func (nopAuditLog) Record(_ schema.Action, _, _ string) error { return nil }

// NopAuditLog returns an AuditLogger that discards every record.
func NopAuditLog() AuditLogger { return nopAuditLog{} }

// MemAuditLog is an in-memory AuditLogger for tests that need to assert on
// the records written. It is not safe for concurrent use.
type MemAuditLog struct {
	Records []AuditRecord
}

// Record appends the entry to the in-memory slice.
func (m *MemAuditLog) Record(action schema.Action, outcome, errMsg string) error {
	m.Records = append(m.Records, AuditRecord{
		Time:    time.Now().UTC(),
		Action:  string(action.Type),
		Target:  action.Target,
		Tier:    string(action.DefaultTier),
		Outcome: outcome,
		Error:   errMsg,
	})
	return nil
}
