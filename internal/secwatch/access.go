package secwatch

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GatherAccess reads fail2ban and recent SSH auth events via journald.
// Returns AccessResult{Available: false} when neither tool is reachable.
// A quiet Base with zero recent logins still reports Available: true as
// long as journalctl ran successfully — empty results ≠ absent tool.
func GatherAccess(ctx context.Context) AccessResult {
	f2b := Fail2banStatus(ctx, "sshd")
	logins, failed, journaldOK := RecentAuth(ctx, 24*time.Hour)

	if !f2b.Available && !journaldOK {
		return AccessResult{Available: false}
	}

	result := AccessResult{
		Available:         true,
		Fail2banAvailable: f2b.Available,
		Fail2banActive:    f2b.Active,
		BannedIPs:         f2b.BannedIPs,
		FailedAttempts:    f2b.TotalFailed,
		RecentLogins:      logins,
	}
	// Prefer fail2ban's count; fall back to the journald-derived count.
	if f2b.Available && f2b.TotalFailed > 0 {
		result.FailedAttempts = f2b.TotalFailed
	} else if failed > 0 {
		result.FailedAttempts = failed
	}

	return result
}

// ---------------------------------------------------------------------------
// fail2ban
// ---------------------------------------------------------------------------

// Fail2banState holds the parsed output of "fail2ban-client status <jail>".
type Fail2banState struct {
	Available   bool
	Active      bool
	BannedIPs   []string
	TotalFailed int
}

// Fail2banStatus runs "fail2ban-client status <jail>" and returns the parsed
// state. Returns Fail2banState{Available: false} when fail2ban-client is not
// installed or returns a non-zero exit code.
func Fail2banStatus(ctx context.Context, jail string) Fail2banState {
	if _, err := exec.LookPath("fail2ban-client"); err != nil {
		return Fail2banState{Available: false}
	}

	out, err := runCmd(ctx, "fail2ban-client", "status", jail)
	if err != nil {
		return Fail2banState{Available: false}
	}

	return parseFail2banStatus(out)
}

// ParseFail2banStatus is the exported form of parseFail2banStatus for Tier-1
// fixture-string testing.
func ParseFail2banStatus(out string) Fail2banState {
	return parseFail2banStatus(out)
}

// parseFail2banStatus parses "fail2ban-client status sshd" output.
// Exported for Tier-1 fixture-string testing.
//
// Example output:
//
//	Status for the jail: sshd
//	|- Filter
//	|  |- Currently failed: 3
//	|  |- Total failed: 47
//	|  `- File list: /var/log/auth.log
//	`- Actions
//	   |- Currently banned: 2
//	   |- Total banned: 5
//	   `- Banned IP list:  1.2.3.4 5.6.7.8
func parseFail2banStatus(out string) Fail2banState {
	fs := Fail2banState{Available: true, Active: true}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)

		switch {
		case strings.Contains(line, "Total failed:"):
			fs.TotalFailed = parseTrailingInt(line)

		case strings.Contains(line, "Banned IP list:"):
			raw := after(line, "Banned IP list:")
			for _, ip := range strings.Fields(raw) {
				if ip != "" {
					fs.BannedIPs = append(fs.BannedIPs, ip)
				}
			}
		}
	}

	return fs
}

// ---------------------------------------------------------------------------
// SSH auth via journald
// ---------------------------------------------------------------------------

// RecentAuth reads recent SSH auth events from journald over the given window.
// Returns (logins, failedCount, available). available is true only when
// journalctl is on PATH AND at least one invocation succeeded — empty results
// on a quiet Base are normal and do not make available false, but a system
// where both journalctl invocations fail is reported as unavailable rather than
// silently returning zero events.
func RecentAuth(ctx context.Context, window time.Duration) ([]LoginEvent, int, bool) {
	if _, err := exec.LookPath("journalctl"); err == nil {
		logins, failed, ok := recentAuthJournald(ctx, window)
		return logins, failed, ok
	}
	return nil, 0, false
}

// recentAuthJournald reads the journal for SSH login events.
// Returns (logins, failedCount, succeeded). succeeded is false only when both
// the ssh and sshd unit invocations fail — a quiet SSH period with zero events
// still returns succeeded=true so a calm Base is not marked as unavailable.
func recentAuthJournald(ctx context.Context, window time.Duration) ([]LoginEvent, int, bool) {
	since := "-" + durationToJournald(window)
	out, err := runCmd(ctx, "journalctl",
		"-u", "ssh", "--since="+since,
		"--no-pager", "-o", "short-iso", "--quiet")
	if err != nil {
		// ssh.service might not exist; try sshd.service.
		out, err = runCmd(ctx, "journalctl",
			"-u", "sshd", "--since="+since,
			"--no-pager", "-o", "short-iso", "--quiet")
		if err != nil {
			// Both invocations failed — journald is either not running or has
			// no sshd unit. Report unavailable rather than silently returning 0.
			return nil, 0, false
		}
	}

	logins, failed := parseJournaldSSH(out)
	return logins, failed, true
}

// ParseJournaldSSH is the exported form of parseJournaldSSH for Tier-1
// fixture-string testing.
func ParseJournaldSSH(out string) ([]LoginEvent, int) {
	return parseJournaldSSH(out)
}

// parseJournaldSSH parses journalctl short-iso output for SSH auth events.
// Exported for Tier-1 fixture-string testing.
//
// Recognized line patterns (sshd log messages):
//   - "Accepted publickey for <user> from <ip> ..."  → successful login
//   - "Accepted password for <user> from <ip> ..."   → successful login
//   - "Failed password for ..."                       → failed attempt
//   - "Invalid user ..."                              → failed attempt
func parseJournaldSSH(out string) (logins []LoginEvent, failedCount int) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()

		// journalctl short-iso lines:
		//   2026-06-24T10:00:00+0000 hostname sshd[1234]: Accepted publickey for ubuntu ...
		// fields[0]=timestamp, fields[1]=hostname, fields[2]=process[pid]:, fields[3..]=message
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// journalctl --output=short-iso emits timestamps in several forms:
		//   2026-06-24T10:00:00.000000+00:00  (RFC 3339 with microseconds — systemd 255+)
		//   2026-06-24T10:00:00+00:00         (RFC 3339 — Ubuntu 24.04)
		//   2026-06-24T10:00:00+0000          (no-colon offset — older systemd)
		// Try most-specific first (RFC3339Nano handles fractional seconds too).
		ts, err := time.Parse(time.RFC3339Nano, fields[0])
		if err != nil {
			ts, err = time.Parse(time.RFC3339, fields[0])
			if err != nil {
				ts, err = time.Parse("2006-01-02T15:04:05-0700", fields[0])
				if err != nil {
					continue
				}
			}
		}

		// Reconstruct the message from fields[3] onwards.
		msg := strings.Join(fields[3:], " ")

		switch {
		case strings.HasPrefix(msg, "Accepted publickey for ") ||
			strings.HasPrefix(msg, "Accepted password for "):
			ev := parseAcceptedLine(msg, ts)
			if ev != nil {
				logins = append(logins, *ev)
			}

		case strings.HasPrefix(msg, "Failed "), strings.HasPrefix(msg, "Invalid user "):
			failedCount++
		}
	}

	return logins, failedCount
}

// parseAcceptedLine extracts a LoginEvent from an "Accepted ... for <user> from <ip>" line.
func parseAcceptedLine(msg string, ts time.Time) *LoginEvent {
	// Extract method: "Accepted publickey for ..." → "publickey"
	method := ""
	msg2 := msg
	if strings.HasPrefix(msg2, "Accepted ") {
		msg2 = msg2[len("Accepted "):]
		spaceIdx := strings.Index(msg2, " for ")
		if spaceIdx >= 0 {
			method = msg2[:spaceIdx]
			msg2 = msg2[spaceIdx+5:]
		}
	}

	// msg2 is now "<user> from <ip> port <port> ..."
	fromIdx := strings.Index(msg2, " from ")
	if fromIdx < 0 {
		return nil
	}
	user := msg2[:fromIdx]
	rest := msg2[fromIdx+6:]

	ip := ""
	ipFields := strings.Fields(rest)
	if len(ipFields) > 0 {
		ip = ipFields[0]
	}

	return &LoginEvent{
		Time:     ts.UTC(),
		User:     user,
		SourceIP: ip,
		Method:   method,
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseTrailingInt extracts the integer after the last colon on a line.
func parseTrailingInt(line string) int {
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(line[idx+1:]))
	return n
}

// after returns the substring of s after the first occurrence of sep,
// trimmed of leading/trailing whitespace.
func after(s, sep string) string {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(s[idx+len(sep):])
}

// durationToJournald converts a time.Duration to a journalctl --since= string
// like "24h" or "1h30m".
func durationToJournald(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return strconv.Itoa(h) + "h"
	}
	return strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m"
}
