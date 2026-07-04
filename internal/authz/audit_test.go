package authz_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

func testAction(t *testing.T) schema.Action {
	t.Helper()
	a, err := schema.NewAction(schema.ActionServiceStart, "ownbase-auth")
	if err != nil {
		t.Fatalf("NewAction: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// AuditLog (file-backed)
// ---------------------------------------------------------------------------

func TestAuditLog_WritesRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	log, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	defer log.Close()

	if err := log.Record(testAction(t), authz.OutcomeApplied, ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func TestAuditLog_RecordsAreJSONLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	log, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}

	action := testAction(t)
	for i := 0; i < 3; i++ {
		if err := log.Record(action, authz.OutcomeApplied, ""); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	log.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	count := 0
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec authz.AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (line: %s)", count+1, err, string(line))
		}
		if rec.Action != string(schema.ActionServiceStart) {
			t.Errorf("line %d: unexpected action %q", count+1, rec.Action)
		}
		if rec.Outcome != authz.OutcomeApplied {
			t.Errorf("line %d: unexpected outcome %q", count+1, rec.Outcome)
		}
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 log lines, got %d", count)
	}
}

func TestAuditLog_AppendOnlyAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	action := testAction(t)

	// First open: write 2 records.
	log1, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog first: %v", err)
	}
	_ = log1.Record(action, authz.OutcomeApplied, "")
	_ = log1.Record(action, authz.OutcomeRolledBack, "")
	log1.Close()

	// Second open: write 1 more record.
	log2, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog second: %v", err)
	}
	_ = log2.Record(action, authz.OutcomeError, "something went wrong")
	log2.Close()

	// Total should be 3 lines.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	lines := 0
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var rec authz.AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("invalid JSON line %d: %v", lines+1, err)
		}
		lines++
	}
	if lines != 3 {
		t.Errorf("expected 3 total records, got %d", lines)
	}
}

func TestAuditLog_ErrorFieldPopulated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	log, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	_ = log.Record(testAction(t), authz.OutcomeError, "container failed to start")
	log.Close()

	data, _ := os.ReadFile(path)
	var rec authz.AuditRecord
	if err := json.Unmarshal(trimNewline(data), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.Error != "container failed to start" {
		t.Errorf("unexpected error field %q", rec.Error)
	}
}

func TestAuditLog_CreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "opt", "ownbase", "logs", "audit.log")

	log, err := authz.NewAuditLog(nested)
	if err != nil {
		t.Fatalf("NewAuditLog with nested path: %v", err)
	}
	log.Close()

	if _, err := os.Stat(nested); err != nil {
		t.Errorf("log file not created at %s: %v", nested, err)
	}
}

// ---------------------------------------------------------------------------
// NopAuditLog
// ---------------------------------------------------------------------------

func TestNopAuditLog_NeverErrors(t *testing.T) {
	log := authz.NopAuditLog()
	if err := log.Record(testAction(t), authz.OutcomeApplied, ""); err != nil {
		t.Errorf("NopAuditLog.Record returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MemAuditLog
// ---------------------------------------------------------------------------

func TestMemAuditLog_RecordsAll(t *testing.T) {
	m := &authz.MemAuditLog{}
	action := testAction(t)
	_ = m.Record(action, authz.OutcomeApplied, "")
	_ = m.Record(action, authz.OutcomeRolledBack, "")

	if len(m.Records) != 2 {
		t.Errorf("expected 2 records, got %d", len(m.Records))
	}
	if m.Records[0].Outcome != authz.OutcomeApplied {
		t.Errorf("first outcome: got %q, want %q", m.Records[0].Outcome, authz.OutcomeApplied)
	}
	if m.Records[1].Outcome != authz.OutcomeRolledBack {
		t.Errorf("second outcome: got %q, want %q", m.Records[1].Outcome, authz.OutcomeRolledBack)
	}
}

// ---------------------------------------------------------------------------
// ReadRecentRecords
// ---------------------------------------------------------------------------

func TestReadRecentRecords_ReturnsLastN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	al, err := authz.NewAuditLog(path)
	if err != nil {
		t.Fatalf("NewAuditLog: %v", err)
	}
	action := testAction(t)
	outcomes := []string{authz.OutcomeApplied, authz.OutcomeRolledBack, authz.OutcomeError}
	for _, o := range outcomes {
		_ = al.Record(action, o, "")
	}
	al.Close()

	got, err := authz.ReadRecentRecords(path, 2)
	if err != nil {
		t.Fatalf("ReadRecentRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Last 2 are rolled_back and error.
	if got[0].Outcome != authz.OutcomeRolledBack {
		t.Errorf("got[0].Outcome = %q, want %q", got[0].Outcome, authz.OutcomeRolledBack)
	}
	if got[1].Outcome != authz.OutcomeError {
		t.Errorf("got[1].Outcome = %q, want %q", got[1].Outcome, authz.OutcomeError)
	}
}

func TestReadRecentRecords_AllWhenNGreaterThanLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	al, _ := authz.NewAuditLog(path)
	action := testAction(t)
	for i := 0; i < 3; i++ {
		_ = al.Record(action, authz.OutcomeApplied, "")
	}
	al.Close()

	got, err := authz.ReadRecentRecords(path, 100)
	if err != nil {
		t.Fatalf("ReadRecentRecords: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestReadRecentRecords_MissingFileReturnsNil(t *testing.T) {
	got, err := authz.ReadRecentRecords("/nonexistent/audit.log", 10)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice for missing file, got %v", got)
	}
}

func TestReadRecentRecords_ZeroNReturnsAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	al, _ := authz.NewAuditLog(path)
	action := testAction(t)
	for i := 0; i < 5; i++ {
		_ = al.Record(action, authz.OutcomeApplied, "")
	}
	al.Close()

	got, err := authz.ReadRecentRecords(path, 0)
	if err != nil {
		t.Fatalf("ReadRecentRecords: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("len = %d, want 5 (n=0 means all)", len(got))
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func trimNewline(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\n' {
		return data[:len(data)-1]
	}
	return data
}
