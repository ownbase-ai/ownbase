package gendb

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PodmanRunner runs commands inside a container with `podman exec`.
type PodmanRunner struct{}

// Exec runs args inside the container and returns its stdout. Stderr is folded
// into the error, since psql reports the reason a statement failed there.
func (PodmanRunner) Exec(ctx context.Context, container string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "podman", append([]string{"exec", container}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}

// Running reports whether the container is up. A container that is restarting,
// created, or gone all count as not running: each means a command sent now
// would fail.
func (PodmanRunner) Running(ctx context.Context, container string) bool {
	out, err := exec.CommandContext(ctx, "podman", "inspect",
		"--format={{.State.Running}}", container).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
