package runtime

// CurrentState represents what is actually running/present on the machine.
// M3 will populate this by querying podman/systemd; M0.5 uses fakes.
type CurrentState struct {
	// RunningContainers is the set of container names currently running.
	RunningContainers map[string]bool
	// PresentNetworks is the set of Podman network names that exist.
	PresentNetworks map[string]bool
	// PresentVolumes is the set of Podman volume names that exist.
	PresentVolumes map[string]bool
}

// EmptyCurrentState returns a CurrentState representing a fresh machine with
// nothing running — the starting point for an install.
func EmptyCurrentState() CurrentState {
	return CurrentState{
		RunningContainers: map[string]bool{},
		PresentNetworks:   map[string]bool{},
		PresentVolumes:    map[string]bool{},
	}
}

// FakeCurrentState returns a CurrentState from explicit lists. Used by
// --fake-current in ownbasectl for local development.
func FakeCurrentState(containerNames []string) CurrentState {
	m := make(map[string]bool, len(containerNames))
	for _, n := range containerNames {
		m[n] = true
	}
	return CurrentState{
		RunningContainers: m,
		PresentNetworks:   map[string]bool{},
		PresentVolumes:    map[string]bool{},
	}
}

// FullFakeCurrentState returns a CurrentState from explicit lists of all
// resource types. Used in tests for convergence checks.
func FullFakeCurrentState(containers, networks, volumes []string) CurrentState {
	toMap := func(ss []string) map[string]bool {
		m := make(map[string]bool, len(ss))
		for _, s := range ss {
			m[s] = true
		}
		return m
	}
	return CurrentState{
		RunningContainers: toMap(containers),
		PresentNetworks:   toMap(networks),
		PresentVolumes:    toMap(volumes),
	}
}
