package backup

// volume.go resolves Podman named-volume host mountpoints and assembles the
// complete restic snapshot path list for a Base. Separated from backup.go so
// the interface and the path-assembly logic can be tested without touching the
// restic CLI.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
)

// VolumeResolver resolves a Podman named-volume to its host mountpoint.
// The production implementation calls `podman volume inspect`.
// Tests inject a fake.
type VolumeResolver interface {
	Resolve(ctx context.Context, volumeName string) (string, error)
}

// PodmanVolumeResolver is the production VolumeResolver.
// It runs: podman volume inspect --format '{{.Mountpoint}}' <name>
type PodmanVolumeResolver struct{}

// Resolve returns the host filesystem path where Podman stores the volume data.
func (PodmanVolumeResolver) Resolve(ctx context.Context, volumeName string) (string, error) {
	out, err := exec.CommandContext(ctx,
		"podman", "volume", "inspect",
		"--format", "{{.Mountpoint}}",
		volumeName,
	).Output()
	if err != nil {
		return "", fmt.Errorf("podman volume inspect %s: %w", volumeName, err)
	}
	mp := strings.TrimSpace(string(out))
	if mp == "" {
		return "", fmt.Errorf("podman volume inspect %s: empty mountpoint", volumeName)
	}
	return mp, nil
}

// coreVolumeNames are the Podman volumes for the OwnBase core package
// (Caddy). Always included in every snapshot regardless of service
// declarations.
var coreVolumeNames = []string{
	"ownbase-core-caddy-data",
}

// BuildPaths assembles the complete restic snapshot path list for a Base.
//
// For each service:
//   - If Volumes is set: for each volume with a non-empty Backup list, resolve
//     the Podman volume mountpoint and append each relative backup entry.
//     "." resolves to the mountpoint itself; "./foo" and "foo" both resolve to
//     mountpoint/foo.
//   - If Volumes is empty (old data_path model): resolve ownbase-<name>-data
//     and include the entire mountpoint (backward compat — no config change
//     required for existing single-volume services).
//
// The core volume (ownbase-core-caddy-data) is always included; a resolve
// error on it is non-fatal (printed to stderr, skipped) because the volume
// may not yet exist on a fresh install.
//
// DefaultPaths (/opt/ownbase/data, /opt/ownbase/secrets, /opt/ownbase/age) are
// always prepended. The result is deduplicated.
func BuildPaths(ctx context.Context, oc *schema.OwnbaseConfig, resolver VolumeResolver) ([]string, error) {
	paths := make([]string, len(DefaultPaths))
	copy(paths, DefaultPaths)

	// Sort service names for deterministic output.
	for _, name := range sortedServiceNames(oc) {
		svc := oc.Services[name]

		if len(svc.Volumes) > 0 {
			for _, v := range svc.Volumes {
				if len(v.Backup) == 0 {
					continue
				}
				volName := fmt.Sprintf("ownbase-%s-%s", name, v.Name)
				mp, err := resolver.Resolve(ctx, volName)
				if err != nil {
					return nil, fmt.Errorf("backup: resolve volume %s for service %s: %w", volName, name, err)
				}
				for _, rel := range v.Backup {
					paths = append(paths, resolveRelative(mp, rel))
				}
			}
		} else {
			// Backward compat: single data volume, whole mountpoint.
			volName := fmt.Sprintf("ownbase-%s-data", name)
			mp, err := resolver.Resolve(ctx, volName)
			if err != nil {
				return nil, fmt.Errorf("backup: resolve volume %s for service %s: %w", volName, name, err)
			}
			paths = append(paths, mp)
		}
	}

	// Core volumes: always included, non-fatal on missing.
	for _, volName := range coreVolumeNames {
		mp, err := resolver.Resolve(ctx, volName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backup: resolve core volume %s: %v (skipping)\n", volName, err)
			continue
		}
		paths = append(paths, mp)
	}

	return dedup(paths), nil
}

// resolveRelative converts a backup entry relative to a volume mountpoint into
// an absolute host path.
//
//	"."       → mp
//	""        → mp
//	"./foo"   → mp/foo
//	"foo/bar" → mp/foo/bar
func resolveRelative(mp, rel string) string {
	rel = strings.TrimPrefix(rel, "./")
	if rel == "." || rel == "" {
		return mp
	}
	return filepath.Join(mp, rel)
}

// dedup returns a new slice with duplicate strings removed, preserving order.
func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// sortedServiceNames returns the service names from oc in sorted order for
// deterministic path assembly.
func sortedServiceNames(oc *schema.OwnbaseConfig) []string {
	if oc == nil {
		return nil
	}
	names := make([]string, 0, len(oc.Services))
	for name := range oc.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
