package runtime

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OwnbasePrefix is the name prefix applied to every OwnBase-managed Podman
// resource. Queries filter by this prefix so the reconciler never sees or
// stops containers/networks/volumes it did not create.
const OwnbasePrefix = "ownbase-"

// OwnbaseCorePrefix is the name prefix for core containers (Caddy)
// managed by bootstrapCore. These are excluded from user reconcile queries
// so the diff loop does not attempt to stop them.
const OwnbaseCorePrefix = "ownbase-core-"

// QuadletContainerPrefix is the name prefix Podman/Quadlet adds to network
// names created from .network Quadlet files without an explicit NetworkName=.
const QuadletContainerPrefix = "systemd-"

// QueryCurrentState asks the local Podman runtime what is actually running
// and returns a CurrentState the reconciler can diff against.
//
// It only reports resources whose name starts with OwnbasePrefix, so
// non-OwnBase containers are invisible to the reconciler. Container names
// are stored without the Quadlet-added "systemd-" prefix so they match the
// service instance names in ownbase.yaml.
//
// Returns an empty CurrentState (not an error) when Podman is not present;
// this allows the agent to run on the dev machine and produce a plan without
// executing it.
func QueryCurrentState() (CurrentState, error) {
	if _, err := exec.LookPath("podman"); err != nil {
		// Podman not available (e.g. dev Mac). Return empty so the caller gets
		// a full plan that it can dry-run or log.
		return EmptyCurrentState(), nil
	}

	// OwnBase Quadlet units all set ContainerName=ownbase-X explicitly, so
	// containers appear as "ownbase-X" (no "systemd-" prefix). Filter directly.
	// We exclude "ownbase-core-*" containers (Caddy) which are managed
	// by bootstrapCore, not the user reconcile loop.
	rawRunning, err := podmanList("ps",
		"--filter", "name=^"+OwnbasePrefix,
		"--filter", "status=running",
		"--format", "{{.Names}}")
	if err != nil {
		return CurrentState{}, fmt.Errorf("query running containers: %w", err)
	}
	running := make([]string, 0, len(rawRunning))
	for _, name := range rawRunning {
		if strings.HasPrefix(name, OwnbaseCorePrefix) {
			continue
		}
		running = append(running, name)
	}

	// Quadlet .network files without an explicit NetworkName= create a network
	// named "systemd-ownbase-X". Strip the Quadlet prefix so reconcile key
	// matches the logical name used in ownbase.yaml / the compiler.
	rawNetworks, err := podmanList("network", "ls",
		"--filter", "name="+OwnbasePrefix,
		"--format", "{{.Name}}")
	if err != nil {
		return CurrentState{}, fmt.Errorf("query networks: %w", err)
	}
	networks := make([]string, 0, len(rawNetworks))
	for _, n := range rawNetworks {
		networks = append(networks, strings.TrimPrefix(n, QuadletContainerPrefix))
	}

	volumes, err := podmanList("volume", "ls", "--filter", "name=^"+OwnbasePrefix, "--format", "{{.Name}}")
	if err != nil {
		return CurrentState{}, fmt.Errorf("query volumes: %w", err)
	}

	return CurrentState{
		RunningContainers: toSet(running),
		PresentNetworks:   toSet(networks),
		PresentVolumes:    toSet(volumes),
	}, nil
}

// podmanList runs a podman subcommand and returns its output lines, trimming
// blank lines and stripping the OwnbasePrefix for callers that store bare names.
func podmanList(args ...string) ([]string, error) {
	out, err := exec.Command("podman", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	var result []string
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		result = append(result, s)
	}
	return result, nil
}

func toSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// JobTimerInfo captures the live systemd state of one scheduled job's timer
// and the most recent activation of the job's container, used by
// internal/explain to surface job status without any package becoming a
// full systemd status client.
type JobTimerInfo struct {
	// Enabled is true when the timer is enabled (persists across reboots).
	// False also covers "not yet installed".
	Enabled bool
	// Active is true when the timer unit itself is active (waiting for its
	// next elapse) — distinct from whether the job it activates is currently
	// running.
	Active bool
	// NextRun is when the timer will next fire. Zero means unknown (e.g.
	// systemctl not present, or the timer has never been installed).
	NextRun time.Time
	// LastRun is when the job's generated .service last exited.
	// Zero means it has never run.
	LastRun time.Time
	// LastResult is systemd's Result= for the last run (e.g. "success",
	// "exit-code"). Empty means unknown/never run.
	LastResult string
}

// QueryJobTimer asks systemd for the live state of one scheduled job's timer
// and the most recent activation of its generated service. name is the
// job's ownbase.yaml key (e.g. "nightly-ingest") — the timer and service
// unit names are derived as "ownbase-job-<name>.timer"/".service", mirroring
// the naming compiler.buildJobContainer and renderTimer produce.
//
// Returns the zero JobTimerInfo (not an error) when systemctl is not present
// (e.g. dev Mac), mirroring QueryCurrentState's Podman-absent behavior.
func QueryJobTimer(name string) JobTimerInfo {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return JobTimerInfo{}
	}
	timerUnit := "ownbase-job-" + name + ".timer"
	serviceUnit := "ownbase-job-" + name + ".service"

	var info JobTimerInfo
	if out, err := exec.Command("systemctl", systemctlModeArgs("is-enabled", timerUnit)...).Output(); err == nil {
		info.Enabled = strings.TrimSpace(string(out)) == "enabled"
	}
	if props, err := systemctlShow(timerUnit, "ActiveState", "NextElapseUSecRealtime"); err == nil {
		info.Active = props["ActiveState"] == "active"
		info.NextRun = parseSystemdTimestamp(props["NextElapseUSecRealtime"])
	}
	if props, err := systemctlShow(serviceUnit, "ActiveExitTimestamp", "Result"); err == nil {
		info.LastRun = parseSystemdTimestamp(props["ActiveExitTimestamp"])
		// systemd defaults Result= to "success" for a loaded-but-never-run
		// unit, so only report it once we know the job has actually run —
		// otherwise a never-run job would misleadingly report LastResult
		// "success" alongside a zero LastRun.
		if !info.LastRun.IsZero() {
			info.LastResult = props["Result"]
		}
	}
	return info
}

// systemctlModeArgs targets the correct service manager: the system manager
// when root, the per-user manager (--user) otherwise — mirroring the
// root/non-root split internal/podman uses for the same reason (root has no
// user D-Bus session in a non-login service context).
func systemctlModeArgs(args ...string) []string {
	if os.Getuid() == 0 {
		return args
	}
	return append([]string{"--user"}, args...)
}

// systemctlShow runs `systemctl show <unit> --property=...` and returns the
// requested properties as a map. Returns an error only when the systemctl
// invocation itself fails (e.g. the unit doesn't exist yet); an
// unrecognized property simply comes back absent from the map.
func systemctlShow(unit string, properties ...string) (map[string]string, error) {
	args := append(systemctlModeArgs("show", unit), "--property="+strings.Join(properties, ","))
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show %s: %w", unit, err)
	}
	result := make(map[string]string, len(properties))
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if idx := strings.Index(line, "="); idx >= 0 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	return result, nil
}

// parseSystemdTimestamp parses a systemd timestamp property (e.g. "Tue
// 2026-07-21 08:00:00 UTC") as reported by `systemctl show`. Returns the
// zero time for "n/a" / "0" / unparseable values — systemd's convention for
// "never" / "unknown".
func parseSystemdTimestamp(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" || v == "0" {
		return time.Time{}
	}
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", v)
	if err != nil {
		return time.Time{}
	}
	return t
}
