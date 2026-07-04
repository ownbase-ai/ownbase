package secwatch

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
)

// GatherExposure runs a full network exposure scan and returns the result.
// Both ss and ufw must be available to produce a valid inventory; if either
// is absent, returns ExposureResult{Available: false} — a partial inventory
// would silently hide exposure and produce a false-clean verdict.
func GatherExposure(ctx context.Context, cfg *schema.OwnbaseConfig, sshPort int) ExposureResult {
	listeners, ssOK := ScanListeners(ctx)
	fw := ReadFirewall(ctx)

	if !ssOK || !fw.Available {
		return ExposureResult{Available: false}
	}

	allowlist := ExpectedAllowlist(cfg, sshPort)
	result := ComputeExposure(listeners, fw, allowlist)
	result.Available = true
	return result
}

// ScanListeners runs ss -tlnpH (TCP) and ss -ulnpH (UDP) and returns parsed
// listeners. TCP is mandatory: if the TCP invocation fails the boolean return
// is false so GatherExposure treats the inventory as unavailable rather than
// empty-and-clean (a failed TCP scan with zero listeners is indistinguishable
// from a machine with no TCP listeners, which is a false-clean security posture).
// UDP is best-effort: a failed UDP invocation is silently skipped so that
// systems where ss -ulnpH is restricted do not block the scan entirely.
// Process names require root; when unavailable the Process field is empty.
func ScanListeners(ctx context.Context) ([]Listener, bool) {
	if _, err := exec.LookPath("ss"); err != nil {
		return nil, false
	}

	var all []Listener

	// TCP is mandatory — a missing TCP inventory cannot be distinguished from
	// a machine with no listeners and would silently report a clean posture.
	tcpOut, err := runCmd(ctx, "ss", "-tlnpH")
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(tcpOut, "\n") {
		if l, ok := parseSSLine(line, "tcp"); ok {
			all = append(all, l)
		}
	}

	// UDP is best-effort — most dangerous services use TCP; a missing UDP
	// inventory is logged but does not invalidate the overall scan.
	if udpOut, err := runCmd(ctx, "ss", "-ulnpH"); err == nil {
		for _, line := range strings.Split(udpOut, "\n") {
			if l, ok := parseSSLine(line, "udp"); ok {
				all = append(all, l)
			}
		}
	}

	return all, true
}

// parseSSLine parses one line of ss -tlnpH / -ulnpH output into a Listener.
// Returns false when the line is empty or malformed.
//
// ss column layout (H = no header):
//
//	State Recv-Q Send-Q Local-Address:Port Peer-Address:Port [Process]
func parseSSLine(line, proto string) (Listener, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return Listener{}, false
	}
	addrPort := fields[3]

	bind, port, ok := splitAddrPort(addrPort)
	if !ok {
		return Listener{}, false
	}

	process := ""
	if len(fields) >= 6 {
		process = extractProcessName(fields[5])
	}

	return Listener{
		Port:    port,
		Proto:   proto,
		Bind:    bind,
		Process: process,
	}, true
}

// splitAddrPort splits an ss Local-Address:Port column into bind address
// and port number. Handles IPv4 ("0.0.0.0:22"), bracketed IPv6 ("[::]:80"),
// and compact IPv6 (":::22") forms.
func splitAddrPort(addrPort string) (bind string, port int, ok bool) {
	// Bracketed IPv6: "[::]:80", "[::1]:443"
	if strings.HasPrefix(addrPort, "[") {
		closeBracket := strings.LastIndex(addrPort, "]")
		if closeBracket < 0 {
			return "", 0, false
		}
		bind = addrPort[1:closeBracket] // strip the brackets
		rest := addrPort[closeBracket+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, false
		}
		p, err := strconv.Atoi(rest[1:])
		if err != nil {
			return "", 0, false
		}
		return bind, p, true
	}

	// Compact IPv6 all-interfaces ":::22" → bind "::", port 22
	if strings.HasPrefix(addrPort, ":::") {
		p, err := strconv.Atoi(addrPort[3:])
		if err != nil {
			return "", 0, false
		}
		return "::", p, true
	}

	// IPv4 "127.0.0.1:22" or "0.0.0.0:80"
	lastColon := strings.LastIndex(addrPort, ":")
	if lastColon < 0 {
		return "", 0, false
	}
	bind = addrPort[:lastColon]
	p, err := strconv.Atoi(addrPort[lastColon+1:])
	if err != nil {
		return "", 0, false
	}
	return bind, p, true
}

// extractProcessName pulls the process name out of the ss process column,
// e.g. `users:(("sshd",pid=812,fd=3))` → "sshd".
func extractProcessName(col string) string {
	// Look for the first quoted name after `(("`.
	start := strings.Index(col, `(("`)
	if start < 0 {
		return ""
	}
	rest := col[start+3:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// FirewallState is the parsed output of ufw status.
type FirewallState struct {
	Available    bool
	Active       bool
	AllowedPorts map[int]bool // inbound ports with ALLOW from Anywhere
}

// ReadFirewall runs ufw status and returns the parsed firewall state.
// Returns FirewallState{Available: false} when ufw is not on PATH.
// Note: ufw status requires root; when run as non-root it will still
// print "Status: inactive" or similar — we parse that correctly.
func ReadFirewall(ctx context.Context) FirewallState {
	if _, err := exec.LookPath("ufw"); err != nil {
		return FirewallState{Available: false}
	}

	out, err := runCmd(ctx, "ufw", "status")
	if err != nil {
		return FirewallState{Available: false}
	}

	return parseUFWStatus(out)
}

// ParseUFWStatus parses the output of "ufw status" into a FirewallState.
// Exported for Tier-1 fixture-string testing.
func ParseUFWStatus(out string) FirewallState {
	return parseUFWStatus(out)
}

func parseUFWStatus(out string) FirewallState {
	fs := FirewallState{
		Available:    true,
		AllowedPorts: make(map[int]bool),
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "Status:") {
			status := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			fs.Active = status == "active"
			continue
		}

		// Parse rule lines: "22/tcp   ALLOW   Anywhere"
		// Skip IPv6 duplicates: "22/tcp (v6)   ALLOW   Anywhere (v6)"
		if strings.Contains(line, "ALLOW") && strings.Contains(line, "Anywhere") &&
			!strings.Contains(line, "(v6)") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			port := parseUFWPort(fields[0])
			if port > 0 {
				fs.AllowedPorts[port] = true
			}
		}
	}

	return fs
}

// parseUFWPort extracts a port number from a UFW "To" field, e.g.:
// "22/tcp" → 22, "80" → 80, "Nginx Full" → 0 (ignored app profile).
func parseUFWPort(field string) int {
	// Strip /tcp or /udp suffix.
	bare := strings.SplitN(field, "/", 2)[0]
	p, err := strconv.Atoi(bare)
	if err != nil {
		return 0
	}
	return p
}

// ExpectedAllowlist returns the set of inbound ports that OwnBase expects
// to be open, derived from ownbase.yaml and the configured SSH port.
//
// Always expected: SSH port, 80 (Caddy HTTP/ACME), 443 (Caddy HTTPS).
// Conditionally expected: Forgejo port when no domain is configured
// (direct-access mode; see schema.ForgejoCoreConfig.EffectivePortOrZeroIfDomain).
func ExpectedAllowlist(cfg *schema.OwnbaseConfig, sshPort int) map[int]bool {
	if sshPort <= 0 {
		sshPort = 22
	}
	allowed := map[int]bool{
		sshPort: true,
		80:      true,
		443:     true,
	}
	if cfg != nil {
		fp := cfg.Core.Forgejo.EffectivePortOrZeroIfDomain()
		if fp > 0 {
			allowed[fp] = true
		}
	}
	return allowed
}

// IsLoopback returns true when the bind address is a loopback address.
// Loopback-only listeners are always considered safe — they are not
// reachable from outside the machine regardless of firewall state.
//
// Exported for use in internal/install/harden.go and for Tier-1 testing.
func IsLoopback(bind string) bool {
	// Fast path for the most common cases.
	switch bind {
	case "127.0.0.1", "::1", "::ffff:127.0.0.1":
		return true
	}
	// Use net.ParseIP for correctness on IPv4-mapped IPv6 and other forms.
	ip := net.ParseIP(bind)
	if ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// IsPublicBind returns true when addr (the LocalAddress:Port column from ss)
// indicates the port is listening on a non-loopback address.
//
// Exported for internal/install/harden.go and Tier-1 testing.
func IsPublicBind(addrPort, port string) bool {
	bind, p, ok := splitAddrPort(addrPort)
	if !ok {
		return false
	}
	if strconv.Itoa(p) != port {
		return false
	}
	return !IsLoopback(bind)
}

// ParseSSLineForTest exposes parseSSLine for Tier-1 testing.
func ParseSSLineForTest(line, proto string) (Listener, bool) {
	return parseSSLine(line, proto)
}

// ComputeExposure annotates listeners with internet-reachability and
// expected-allowlist status, and counts unexpected internet-reachable ports.
func ComputeExposure(listeners []Listener, fw FirewallState, allowlist map[int]bool) ExposureResult {
	out := ExposureResult{
		FirewallActive: fw.Active,
		Listeners:      make([]Listener, len(listeners)),
	}

	for i, l := range listeners {
		// A non-loopback listener is internet-reachable if UFW allows that
		// port inbound. When UFW is inactive there is no firewall to block
		// any port, so every non-loopback listener is reachable.
		l.InternetReachable = !IsLoopback(l.Bind) && (fw.AllowedPorts[l.Port] || !fw.Active)
		l.Expected = allowlist[l.Port]
		if l.InternetReachable && !l.Expected {
			out.UnexpectedCount++
		}
		out.Listeners[i] = l
	}

	return out
}

// runCmd executes a command and returns its combined output as a string.
func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return string(out), nil
}
