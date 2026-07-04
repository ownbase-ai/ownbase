// Package runtime contains the query/apply adapters for the podman/systemd
// runtime. The Quadlet and Caddy *emitters* live in internal/compiler (the
// typed-builder render stage); this package owns the read/write side:
//
//   - CurrentState (what is actually running now)
//   - M3: real podman/systemd apply and query adapters
package runtime
