package main

// Tier-1 tests for `checkup --verify`. The drill's verdict has to survive into
// the exit code in both output modes: --json is what a scripted caller uses, so
// that is precisely where a swallowed failure would go unnoticed.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// drillServer answers /backup/verify with a streamed drill and /status with an
// empty payload, which the report renderers handle without a Base.
func drillServer(passed bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backup/verify":
			_, _ = io.WriteString(w, "==> Restoring snapshot 4f2a91c into an isolated directory\n")
			if passed {
				_, _ = io.WriteString(w, `---RESULT---{"passed":true,"checks":[{"name":"restic-check","passed":true,"detail":"repository integrity OK"}]}`+"\n")
				_, _ = io.WriteString(w, "---OK---\n")
				return
			}
			// No ---OK---: the drill ran and a check failed.
			_, _ = io.WriteString(w, `---RESULT---{"passed":false,"checks":[{"name":"postgres-recovery","passed":false,"detail":"recovery did not finish within 10m"}]}`+"\n")
		case "/status":
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCheckupVerify_FailedDrillIsAnErrorInBothModes(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		mode := "text"
		if jsonOut {
			mode = "json"
		}
		t.Run(mode, func(t *testing.T) {
			ts := drillServer(false)
			defer ts.Close()

			var err error
			out := captureStdout(t, func() {
				err = checkup(fakeConn(ts, "token"), "mybase", jsonOut, true)
			})

			if err == nil {
				t.Fatal("checkup returned nil after a failed drill — a scripted caller would read success")
			}
			if !strings.Contains(err.Error(), "postgres-recovery") {
				t.Errorf("error does not name the check that failed: %v", err)
			}
			if jsonOut {
				assertSingleJSONDocument(t, out, true)
			}
		})
	}
}

// --json has to be one document. The drill result and /status are two payloads,
// and printing both in sequence gives a stream that no ordinary JSON reader
// accepts — the caller most likely to use --json is the one least able to
// notice.
func assertSingleJSONDocument(t *testing.T, out string, wantVerify bool) {
	t.Helper()
	var payload struct {
		Verify struct {
			Passed bool `json:"passed"`
			Checks []struct {
				Name string `json:"name"`
			} `json:"checks"`
		} `json:"verify"`
		Status map[string]any `json:"status"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (err=%v):\n%s", err, out)
	}
	if wantVerify && len(payload.Verify.Checks) == 0 {
		t.Errorf("drill result missing from the document:\n%s", out)
	}
	if payload.Status == nil {
		t.Errorf("status payload missing from the document:\n%s", out)
	}
}

// Without --verify the payload stays the /status body verbatim, so anything
// already parsing `checkup --json` keeps working.
func TestCheckupJSON_IsUnchangedWithoutVerify(t *testing.T) {
	ts := drillServer(true)
	defer ts.Close()

	out := captureStdout(t, func() {
		if err := checkup(fakeConn(ts, "token"), "mybase", true, false); err != nil {
			t.Fatalf("checkup: %v", err)
		}
	})
	if strings.TrimSpace(out) != "{}" {
		t.Errorf("output = %q, want the /status body unchanged", strings.TrimSpace(out))
	}
}

func TestCheckupVerify_PassingDrillReturnsNil(t *testing.T) {
	ts := drillServer(true)
	defer ts.Close()

	var err error
	out := captureStdout(t, func() {
		err = checkup(fakeConn(ts, "token"), "mybase", false, true)
	})
	if err != nil {
		t.Fatalf("checkup after a passing drill: %v", err)
	}
	if !strings.Contains(out, "Restore verified") {
		t.Errorf("output does not confirm the drill passed:\n%s", out)
	}
}
