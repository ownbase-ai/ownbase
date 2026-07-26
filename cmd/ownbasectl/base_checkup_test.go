package main

// Tier-1 tests for `checkup --verify`. The drill's verdict has to survive into
// the exit code in both output modes: --json is what a scripted caller uses, so
// that is precisely where a swallowed failure would go unnoticed.

import (
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
			if jsonOut && !strings.Contains(out, `"passed":false`) {
				t.Errorf("--json did not print the drill result payload:\n%s", out)
			}
		})
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
