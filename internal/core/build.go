package core

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

//go:embed caddy/Dockerfile
var caddyDockerfile []byte

// LocalCaddyImage is the tag bootstrap/upgrade build into. Not pulled from a
// registry — rebuilt on the Base from the embedded Dockerfile whenever the
// daemon starts or `ownbasectl upgrade --apply` runs.
const LocalCaddyImage = "localhost/ownbase-core-caddy:local"

// CaddyImagePresent reports whether LocalCaddyImage already exists locally.
func CaddyImagePresent() bool {
	err := exec.Command("podman", "image", "exists", LocalCaddyImage).Run()
	return err == nil
}

// BuildCaddyImage builds the hardened Caddy image from the embedded Dockerfile
// and tags it LocalCaddyImage. Progress is written to w when non-nil.
func BuildCaddyImage(ctx context.Context, w interface{ Write([]byte) (int, error) }) error {
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman not found: %w", err)
	}

	dir, err := os.MkdirTemp("", "ownbase-caddy-build-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), caddyDockerfile, 0o644); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}

	// Bound the build so a stuck pull cannot hang the daemon forever.
	buildCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		buildCtx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}

	cmd := exec.CommandContext(buildCtx, "podman", "build",
		"--pull=newer",
		"-t", LocalCaddyImage,
		"-f", filepath.Join(dir, "Dockerfile"),
		dir,
	)
	var buf bytes.Buffer
	if w != nil {
		cmd.Stdout = w
		cmd.Stderr = w
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	}
	if err := cmd.Run(); err != nil {
		if buf.Len() > 0 {
			return fmt.Errorf("podman build caddy: %w\n%s", err, buf.String())
		}
		return fmt.Errorf("podman build caddy: %w", err)
	}
	return nil
}
