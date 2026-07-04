package secrets

// Injector is the M3/Tier-2 seam for delivering a SecretSet to a running
// Podman container. The V1 implementation calls "podman secret create" for
// each value in the set so the Quadlet unit's Secret= directives resolve at
// container start without writing plaintext to disk.
//
// This interface exists in M2 so M3 can implement it without touching the
// secrets package. The apply step in M3 will:
//  1. Call Issue() to obtain a scoped SecretSet.
//  2. Pass the SecretSet to an Injector.Inject() call.
//  3. Start the Quadlet unit; Podman resolves the Secret= directives to the
//     values the Injector registered.
//
// The injector must be called before the container unit is activated, and
// must clean up (podman secret rm) after the container stops.
type Injector interface {
	// Inject registers each secret in ss with the Podman runtime under the
	// canonical name "ownbase-{repoKey}-{secretName}". Returns an error if
	// any registration fails; the caller must not start the container in
	// that case.
	Inject(repoKey string, ss SecretSet) error

	// Remove deletes the Podman secrets previously registered for repoKey.
	// Called after the container stops or on rollback.
	Remove(repoKey string, names []string) error
}

// NoopInjector is a do-nothing Injector for use in dry-run and M0.5/M2
// contexts where Podman is not available. It records the calls it would have
// made so tests can assert them.
type NoopInjector struct {
	Injected []string
	Removed  []string
}

// Inject records that it would inject ss for repoKey.
func (n *NoopInjector) Inject(repoKey string, ss SecretSet) error {
	for _, name := range ss.Names() {
		n.Injected = append(n.Injected, "ownbase-"+repoKey+"-"+name)
	}
	return nil
}

// Remove records that it would remove names for repoKey.
func (n *NoopInjector) Remove(repoKey string, names []string) error {
	for _, name := range names {
		n.Removed = append(n.Removed, "ownbase-"+repoKey+"-"+name)
	}
	return nil
}
