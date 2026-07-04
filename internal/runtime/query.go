package runtime

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// OwnbasePrefix is the name prefix applied to every OwnBase-managed Podman
// resource. Queries filter by this prefix so the reconciler never sees or
// stops containers/networks/volumes it did not create.
const OwnbasePrefix = "ownbase-"

// OwnbaseCorePrefix is the name prefix for core containers (Forgejo, Caddy)
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
	// We exclude "ownbase-core-*" containers (Forgejo, Caddy) which are managed
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
