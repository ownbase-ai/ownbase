package sshsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recording format is a contract with tools that are not ours — `asciinema
// play` has to render these files, and the desktop app hands them to asciinema's
// own player verbatim. So these tests read the file the way an outside reader
// would rather than through the writer that produced it.

func recordInto(t *testing.T, meta Meta) *Recorder {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(InvokerEnv, "")
	if meta.ID == "" {
		meta.ID = NewID()
	}
	if meta.Base == "" {
		meta.Base = "mybase"
	}
	rec, err := NewRecorder(meta, 120, 40)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return rec
}

func TestRecordingIsAsciicastV2(t *testing.T) {
	rec := recordInto(t, Meta{Command: "uptime"})
	rec.Output([]byte("load average: 0.14\r\n"))
	if err := rec.Finish(0, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	data, err := os.ReadFile(rec.Meta().CastPath)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want a header and one event:\n%s", len(lines), data)
	}

	var header struct {
		Version int    `json:"version"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Title   string `json:"title"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	if header.Version != 2 {
		t.Errorf("version = %d, want 2", header.Version)
	}
	if header.Width != 120 || header.Height != 40 {
		t.Errorf("terminal = %dx%d, want 120x40 — a replay would wrap wrongly", header.Width, header.Height)
	}
	if !strings.Contains(header.Title, "uptime") {
		t.Errorf("title = %q, does not say what was run", header.Title)
	}

	var event []any
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatalf("event is not a JSON array: %v", err)
	}
	if len(event) != 3 {
		t.Fatalf("event has %d fields, want [time, kind, text]", len(event))
	}
	if event[1] != "o" {
		t.Errorf("kind = %v, want \"o\"", event[1])
	}
	if event[2] != "load average: 0.14\r\n" {
		t.Errorf("text = %q, want the bytes verbatim", event[2])
	}
}

// Input is the half that makes the trail auditable: a command that printed
// nothing leaves no output event, and dropping input would hide it entirely.
func TestRecordingKeepsInputAndControlBytes(t *testing.T) {
	rec := recordInto(t, Meta{Interactive: true})
	rec.Input([]byte("rm -rf /tmp/x\r"))
	rec.Input([]byte{0x03}) // ctrl-c
	if err := rec.Finish(130, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	data, err := os.ReadFile(rec.Meta().CastPath)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	var kinds []string
	var texts []string
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if i == 0 {
			continue
		}
		var event []any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event %d is not JSON: %v", i, err)
		}
		kind, _ := event[1].(string)
		text, _ := event[2].(string)
		kinds = append(kinds, kind)
		texts = append(texts, text)
	}
	if len(kinds) != 2 || kinds[0] != "i" || kinds[1] != "i" {
		t.Fatalf("kinds = %v, want two input events", kinds)
	}
	if texts[0] != "rm -rf /tmp/x\r" {
		t.Errorf("typed command = %q, want it verbatim", texts[0])
	}
	if texts[1] != "\x03" {
		t.Errorf("control byte = %q, want it to survive the round trip", texts[1])
	}
}

// A session killed mid-flight has to still appear in the trail. If the sidecar
// were only written at the end, the shells worth auditing most — the ones that
// crashed the machine — would be the ones missing.
func TestSidecarExistsBeforeTheSessionEnds(t *testing.T) {
	rec := recordInto(t, Meta{Host: "203.0.113.10", User: "ubuntu"})

	found, err := List("mybase")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("List returned %d sessions before Finish, want 1", len(found))
	}
	if found[0].EndedAt != nil {
		t.Error("an unfinished session reports an end time")
	}
	if found[0].ExitCode != -1 {
		t.Errorf("exit code = %d before the session ended, want -1 (no status)", found[0].ExitCode)
	}

	if err := rec.Finish(0, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	found, err = List("mybase")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("List returned %d sessions after Finish, want 1", len(found))
	}
	if found[0].EndedAt == nil || found[0].ExitCode != 0 {
		t.Errorf("outcome not recorded: %+v", found[0])
	}
}

// A recording can hold anything typed at a prompt, including a pasted secret.
func TestRecordingsAreOwnerReadableOnly(t *testing.T) {
	rec := recordInto(t, Meta{})
	rec.Output([]byte("hello"))
	if err := rec.Finish(0, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	for _, path := range []string{
		rec.Meta().CastPath,
		strings.TrimSuffix(rec.Meta().CastPath, castExt) + metaExt,
		filepath.Dir(rec.Meta().CastPath),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm() & 0o077; perm != 0 {
			t.Errorf("%s is mode %o — readable by other accounts on this machine", path, info.Mode().Perm())
		}
	}
}

func TestTranscriptIsOutputOnly(t *testing.T) {
	rec := recordInto(t, Meta{Interactive: true})
	rec.Input([]byte("whoami\r"))
	rec.Output([]byte("root\r\n"))
	if err := rec.Finish(0, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	text, err := Transcript(rec.Meta().CastPath)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if text != "root\r\n" {
		t.Errorf("transcript = %q, want just what the Base sent", text)
	}
}

// Two sessions opened in the same second must get different ids. A recording is
// created O_EXCL, so a collision does not merely confuse the list — it fails the
// shell the operator was trying to open.
func TestNewIDIsSortableAndDistinct(t *testing.T) {
	seen := make(map[string]bool)
	var ids []string
	for range 2000 {
		id := NewID()
		ids = append(ids, id)
		seen[id] = true
	}
	if len(seen) != len(ids) {
		t.Errorf("got %d distinct ids out of %d", len(seen), len(ids))
	}
	// The timestamp prefix is what makes "newest first" cheap and the directory
	// readable, so it has to stay lexically ordered.
	for i := 1; i < len(ids); i++ {
		if ids[i][:16] < ids[i-1][:16] {
			t.Fatalf("ids are not chronological: %q then %q", ids[i-1], ids[i])
		}
	}
}

func TestInvokerLabelsWhoOpenedTheSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(InvokerEnv, "app")
	rec, err := NewRecorder(Meta{ID: NewID(), Base: "mybase"}, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() {
		if err := rec.Finish(0, nil); err != nil {
			t.Errorf("Finish: %v", err)
		}
	})
	if got := rec.Meta().Invoker; got != "app" {
		t.Errorf("invoker = %q, want %q", got, "app")
	}
}
