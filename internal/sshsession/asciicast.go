// Package sshsession records every shell OwnBase opens on a Base.
//
// The reason this exists: an agent with root on a machine is exactly as
// trustworthy as your ability to see what it did. Reading a Base's own logs
// tells you what the machine noticed; a session recording tells you what was
// actually typed and what came back, including the commands that left no trace.
// So every shell goes through `ownbasectl ssh`, and every shell is recorded.
//
// The format is asciicast v2 — the plain, line-delimited JSON format
// asciinema uses. One JSON object of metadata, then one JSON array per chunk of
// output: [elapsed_seconds, "o", "text"] for output and "i" for input. It is
// text, it is documented, and `asciinema play` renders it without OwnBase
// installed. A proprietary transcript would have made the audit trail depend on
// us, which defeats the purpose of having one.
package sshsession

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ownbase/ownbase/internal/vault"
)

// SessionsDirName is the directory under ~/.ownbase holding recordings.
const SessionsDirName = "sessions"

// castExt is the file extension for an asciicast v2 recording.
const castExt = ".cast"

// metaExt is the extension of the sidecar file holding a session's metadata.
// Kept beside the recording rather than inside it so listing a thousand
// sessions does not mean parsing a thousand multi-megabyte transcripts.
const metaExt = ".json"

// Meta describes one recorded session. It is written twice: once when the
// session starts (so a crashed session still leaves a trace) and once when it
// ends, with the outcome filled in.
type Meta struct {
	// ID is the recording's basename, e.g. "20260727T110455Z-3f9c".
	ID string `json:"id"`
	// Base is the Base the session was opened on.
	Base string `json:"base"`
	// Host and User are where and as whom.
	Host string `json:"host"`
	User string `json:"user"`
	// Command is the one-shot command, or "" for an interactive shell.
	Command string `json:"command,omitempty"`
	// Interactive reports whether a PTY was allocated.
	Interactive bool `json:"interactive"`
	// StartedAt and EndedAt bound the session.
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// ExitCode is the remote command's exit status, or -1 when the session
	// ended without one (transport failure, killed).
	ExitCode int `json:"exit_code"`
	// Error is set when the session itself failed rather than the remote
	// command exiting non-zero.
	Error string `json:"error,omitempty"`
	// Bytes is the size of the recorded transcript.
	Bytes int64 `json:"bytes"`
	// Invoker names what opened the session, so a shell an agent opened is
	// distinguishable from one a human opened. Taken from
	// $OWNBASE_INVOKER when set, else "cli".
	Invoker string `json:"invoker,omitempty"`
	// CastPath is the recording's location on disk.
	CastPath string `json:"cast_path"`
}

// Duration returns how long the session lasted, or 0 if it is still open.
func (m Meta) Duration() time.Duration {
	if m.EndedAt == nil {
		return 0
	}
	return m.EndedAt.Sub(m.StartedAt)
}

// InvokerEnv lets the caller label who opened a session — the desktop app sets
// it to "app", a coding agent can set it to its own name. It is a label for the
// audit trail, not a permission: the recording happens either way.
const InvokerEnv = "OWNBASE_INVOKER"

// Recorder writes an asciicast v2 stream plus its metadata sidecar.
type Recorder struct {
	meta  Meta
	cast  *os.File
	enc   *json.Encoder
	start time.Time

	mu      sync.Mutex
	written int64
	closed  bool
}

// Root returns the directory holding every Base's recordings.
func Root() (string, error) { return vault.StatePath(SessionsDirName) }

// Dir returns the directory holding recordings for a Base.
func Dir(base string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, base), nil
}

// NewRecorder starts a recording for a session. width and height describe the
// terminal, which asciinema needs to replay the stream at the right size.
func NewRecorder(meta Meta, width, height int) (*Recorder, error) {
	dir, err := Dir(meta.Base)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	meta.StartedAt = time.Now()
	meta.ExitCode = -1
	if meta.Invoker == "" {
		meta.Invoker = invokerLabel()
	}
	meta.CastPath = filepath.Join(dir, meta.ID+castExt)

	// A recording can contain anything the operator typed, including a secret
	// pasted into a prompt, so it is owner-readable only.
	f, err := os.OpenFile(meta.CastPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create recording %s: %w", meta.CastPath, err)
	}

	r := &Recorder{meta: meta, cast: f, enc: json.NewEncoder(f), start: meta.StartedAt}
	header := map[string]any{
		"version":   2,
		"width":     width,
		"height":    height,
		"timestamp": meta.StartedAt.Unix(),
		"title":     recordingTitle(meta),
		"env":       map[string]string{"TERM": os.Getenv("TERM"), "SHELL": ""},
	}
	if err := r.enc.Encode(header); err != nil {
		f.Close()
		return nil, fmt.Errorf("write recording header: %w", err)
	}
	// Write the sidecar now, so a session killed mid-flight still shows up in
	// the audit trail as one that started and never finished.
	if err := r.writeMeta(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func recordingTitle(m Meta) string {
	if m.Command != "" {
		return fmt.Sprintf("ownbasectl ssh %s -- %s", m.Base, m.Command)
	}
	return "ownbasectl ssh " + m.Base
}

func invokerLabel() string {
	if v := strings.TrimSpace(os.Getenv(InvokerEnv)); v != "" {
		return v
	}
	return "cli"
}

// NewID returns a sortable, collision-resistant recording id.
//
// The suffix is random rather than derived from the clock: the nanosecond field
// is only as fine as the platform's timer, so two sessions opened in the same
// second collided often enough to matter — and a collision is not a cosmetic
// problem here, because the recording is created O_EXCL and the session would
// fail outright.
func NewID() string {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// A machine whose CSPRNG is unavailable has larger problems, and
		// failing to open a shell over it would help nobody.
		return fmt.Sprintf("%s-%06x", time.Now().UTC().Format("20060102T150405Z"), time.Now().UnixNano()&0xffffff)
	}
	return fmt.Sprintf("%s-%x", time.Now().UTC().Format("20060102T150405Z"), suffix)
}

// Meta returns the recording's current metadata.
func (r *Recorder) Meta() Meta { return r.meta }

// Output records bytes the Base sent.
func (r *Recorder) Output(p []byte) { r.event("o", p) }

// Input records bytes the operator sent. Recording input is what makes the
// trail auditable rather than merely informative: output alone cannot show a
// command that produced none.
func (r *Recorder) Input(p []byte) { r.event("i", p) }

func (r *Recorder) event(kind string, p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	elapsed := time.Since(r.start).Seconds()
	// Encoding a []any gives the asciicast array form directly; the string is
	// JSON-escaped by encoding/json, so control bytes survive a round trip.
	if err := r.enc.Encode([]any{elapsed, kind, string(p)}); err != nil {
		return
	}
	r.written += int64(len(p))
}

// Finish closes the recording, recording the outcome. exitCode is the remote
// command's status, or -1 if there was none.
func (r *Recorder) Finish(exitCode int, sessionErr error) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	written := r.written
	r.mu.Unlock()

	now := time.Now()
	r.meta.EndedAt = &now
	r.meta.ExitCode = exitCode
	r.meta.Bytes = written
	if sessionErr != nil {
		r.meta.Error = sessionErr.Error()
	}

	cerr := r.cast.Close()
	if err := r.writeMeta(); err != nil {
		return err
	}
	return cerr
}

func (r *Recorder) writeMeta() error {
	path := strings.TrimSuffix(r.meta.CastPath, castExt) + metaExt
	data, err := json.MarshalIndent(r.meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Tee wraps w so everything written through it is also recorded as output.
func (r *Recorder) Tee(w io.Writer) io.Writer { return &teeWriter{w: w, rec: r.Output} }

// TeeInput wraps w so everything written through it is also recorded as input.
func (r *Recorder) TeeInput(w io.Writer) io.Writer { return &teeWriter{w: w, rec: r.Input} }

type teeWriter struct {
	w   io.Writer
	rec func([]byte)
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.rec(p[:n])
	}
	return n, err
}

// List returns the recorded sessions for one Base, or for every Base when base
// is empty, newest first.
func List(base string) ([]Meta, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}

	bases := []string{base}
	if base == "" {
		entries, rerr := os.ReadDir(root)
		if os.IsNotExist(rerr) {
			return nil, nil
		}
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", root, rerr)
		}
		bases = nil
		for _, e := range entries {
			if e.IsDir() {
				bases = append(bases, e.Name())
			}
		}
	}

	var all []Meta
	for _, b := range bases {
		dir := filepath.Join(root, b)
		entries, rerr := os.ReadDir(dir)
		if os.IsNotExist(rerr) {
			continue
		}
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", dir, rerr)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != metaExt {
				continue
			}
			data, ferr := os.ReadFile(filepath.Join(dir, e.Name()))
			if ferr != nil {
				continue
			}
			var m Meta
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			all = append(all, m)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	return all, nil
}

// Find returns the metadata for one recording id, searching every Base when
// base is empty.
func Find(base, id string) (Meta, error) {
	sessions, err := List(base)
	if err != nil {
		return Meta{}, err
	}
	for _, m := range sessions {
		if m.ID == id {
			return m, nil
		}
	}
	return Meta{}, fmt.Errorf("no recorded session with id %q — run 'ownbasectl sessions list'", id)
}

// Transcript renders a recording as plain text: the output stream with timing
// and input events dropped. This is what a person (or an agent) reads when they
// want to know what happened, without an asciinema player.
func Transcript(castPath string) (string, error) {
	f, err := os.Open(castPath)
	if err != nil {
		return "", fmt.Errorf("open recording %s: %w", castPath, err)
	}
	defer f.Close()

	var b strings.Builder
	dec := json.NewDecoder(f)
	first := true
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		if first {
			first = false // the header object
			continue
		}
		var event []any
		if json.Unmarshal(raw, &event) != nil || len(event) < 3 {
			continue
		}
		kind, _ := event[1].(string)
		text, _ := event[2].(string)
		if kind == "o" {
			b.WriteString(text)
		}
	}
	return b.String(), nil
}
