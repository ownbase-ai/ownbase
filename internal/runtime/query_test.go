package runtime

import (
	"testing"
	"time"
)

// TestParseSystemdTimestamp_ValidValue covers the exact format `systemctl
// show` reports for timestamp properties (ExecMainExitTimestamp,
// NextElapseUSecRealtime, etc.), confirmed against a live run on the
// production Base: "Tue 2026-07-21 17:37:19 UTC".
func TestParseSystemdTimestamp_ValidValue(t *testing.T) {
	got := parseSystemdTimestamp("Tue 2026-07-21 17:37:19 UTC")
	want := time.Date(2026, 7, 21, 17, 37, 19, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseSystemdTimestamp = %v, want %v", got, want)
	}
}

func TestParseSystemdTimestamp_NeverRun(t *testing.T) {
	for _, v := range []string{"", "n/a", "0"} {
		if got := parseSystemdTimestamp(v); !got.IsZero() {
			t.Errorf("parseSystemdTimestamp(%q) = %v, want zero time", v, got)
		}
	}
}

func TestParseSystemdTimestamp_Unparseable(t *testing.T) {
	if got := parseSystemdTimestamp("not a timestamp"); !got.IsZero() {
		t.Errorf("parseSystemdTimestamp(garbage) = %v, want zero time", got)
	}
}
