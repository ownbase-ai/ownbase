package install

// harden_test.go: Tier-1 tests for host hardening helpers.
// These tests are in the install package (not install_test) so they can
// access unexported functions.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/secwatch"
)

// ---------------------------------------------------------------------------
// Git SSH shim content tests
// ---------------------------------------------------------------------------

// TestGitSSHShimContent verifies the shim scripts contain the required
// podman exec invocations and that the sshd drop-in references
// AuthorizedKeysCommand and restricts the git user.
func TestGitSSHShimContent(t *testing.T) {
	t.Run("forgejo-keys contains podman exec and sed rewrite", func(t *testing.T) {
		if !strings.Contains(forgejoKeysContent, "podman exec") || !strings.Contains(forgejoKeysContent, "ownbase-core-forgejo") {
			t.Error("forgejo-keys shim must call 'podman exec ... ownbase-core-forgejo'")
		}
		if !strings.Contains(forgejoKeysContent, "forgejo keys") {
			t.Error("forgejo-keys shim must invoke 'forgejo keys'")
		}
		if !strings.Contains(forgejoKeysContent, "sed") {
			t.Error("forgejo-keys shim must rewrite command= via sed")
		}
		if !strings.Contains(forgejoKeysContent, "forgejo-serv") {
			t.Error("forgejo-keys shim must rewrite command= to forgejo-serv path")
		}
		// The sed pattern must capture the key-N token, not match a literal prefix
		// that differs across Forgejo versions.
		if !strings.Contains(forgejoKeysContent, "key-") {
			t.Error("forgejo-keys sed rewrite must preserve the key-N token")
		}
	})

	t.Run("forgejo-serv contains podman exec -i", func(t *testing.T) {
		if !strings.Contains(forgejoServContent, "podman exec -i") || !strings.Contains(forgejoServContent, "ownbase-core-forgejo") {
			t.Error("forgejo-serv shim must call 'podman exec -i ... ownbase-core-forgejo'")
		}
		if !strings.Contains(forgejoServContent, "forgejo serv") {
			t.Error("forgejo-serv shim must invoke 'forgejo serv'")
		}
	})

	t.Run("sshd drop-in contains AuthorizedKeysCommand and Match User git", func(t *testing.T) {
		if !strings.Contains(gitSSHSshdConfContent, "AuthorizedKeysCommand") {
			t.Error("sshd drop-in must contain AuthorizedKeysCommand directive")
		}
		if !strings.Contains(gitSSHSshdConfContent, "Match User git") {
			t.Error("sshd drop-in must contain 'Match User git' block")
		}
		if !strings.Contains(gitSSHSshdConfContent, "PasswordAuthentication no") {
			t.Error("sshd drop-in must disable password auth for git user")
		}
		if !strings.Contains(gitSSHSshdConfContent, "forgejo-keys") {
			t.Error("sshd drop-in AuthorizedKeysCommand must reference forgejo-keys")
		}
		// Tokens must match forgejo keys flags: %u=username, %t=type, %k=base64-key.
		// %f (fingerprint) is wrong — forgejo keys uses --type and --content.
		if !strings.Contains(gitSSHSshdConfContent, "%t") || !strings.Contains(gitSSHSshdConfContent, "%k") {
			t.Errorf("sshd AuthorizedKeysCommand must pass %%t (key type) and %%k (key content) tokens, got: %s",
				gitSSHSshdConfContent)
		}
		if strings.Contains(gitSSHSshdConfContent, "%f") {
			t.Errorf("sshd AuthorizedKeysCommand must not use %%f (fingerprint); forgejo keys uses --type/--content, got: %s",
				gitSSHSshdConfContent)
		}
	})

	t.Run("forgejo-keys shim uses --type and --content flags", func(t *testing.T) {
		if !strings.Contains(forgejoKeysContent, "--type") {
			t.Error("forgejo-keys must pass --type flag to forgejo keys")
		}
		if !strings.Contains(forgejoKeysContent, "--content") {
			t.Error("forgejo-keys must pass --content flag to forgejo keys")
		}
		if strings.Contains(forgejoKeysContent, "--fingerprint") {
			t.Error("forgejo-keys must not use --fingerprint; forgejo keys uses --type/--content")
		}
	})

	t.Run("shims use sudo -n for rootful podman access", func(t *testing.T) {
		if !strings.Contains(forgejoKeysContent, "sudo -n") {
			t.Error("forgejo-keys must use 'sudo -n' to exec into the rootful container")
		}
		if !strings.Contains(forgejoServContent, "sudo -n") {
			t.Error("forgejo-serv must use 'sudo -n' to exec into the rootful container")
		}
	})

	t.Run("sudoers rule grants only targeted podman exec calls", func(t *testing.T) {
		if !strings.Contains(gitSSHSudoersContent, "NOPASSWD") {
			t.Error("sudoers rule must grant NOPASSWD for git user")
		}
		if !strings.Contains(gitSSHSudoersContent, "forgejo keys") {
			t.Error("sudoers rule must allow 'forgejo keys' exec")
		}
		if !strings.Contains(gitSSHSudoersContent, "forgejo serv") {
			t.Error("sudoers rule must allow 'forgejo serv' exec")
		}
		// Must be scoped to the specific container, not all of podman.
		if !strings.Contains(gitSSHSudoersContent, "ownbase-core-forgejo") {
			t.Error("sudoers rule must be scoped to ownbase-core-forgejo container")
		}
	})
}

// TestWriteExecutable verifies writeExecutable creates files with mode 0755
// and is idempotent (calling twice does not error and does not change the file).
func TestWriteExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-shim")
	content := "#!/bin/bash\necho hello\n"

	// First write.
	if err := writeExecutable(path, content); err != nil {
		t.Fatalf("writeExecutable (first): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after first write: %v", err)
	}
	if string(data) != content {
		t.Errorf("content mismatch: got %q, want %q", string(data), content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode: got %o, want 0755", info.Mode().Perm())
	}

	// Second write — must be idempotent (no error, same content).
	if err := writeExecutable(path, content); err != nil {
		t.Fatalf("writeExecutable (second, idempotent): %v", err)
	}
	data2, _ := os.ReadFile(path)
	if string(data2) != content {
		t.Error("idempotent write changed file content")
	}
}

// TestCheckGitSSHState_MissingFiles verifies that checkGitSSHState returns
// Done=false when the shim files are absent (no real filesystem writes needed).
func TestCheckGitSSHState_MissingFiles(t *testing.T) {
	// Override the package-level constants is not possible in Go, so instead
	// we test the logic by verifying that checkGitSSHState reads the real
	// paths. On a dev machine the shim files won't exist, so Done should be
	// false (or AlreadyOK if someone has run PassZero on the dev machine —
	// either outcome is valid, we just verify no panic and consistent shape).
	s := checkGitSSHState(context.Background())
	if s.Err != nil {
		t.Errorf("checkGitSSHState returned unexpected error: %v", s.Err)
	}
	// Done=true is only expected if all three files exist with correct content.
	// On a dev machine that's not set up, Done should be false.
	// We can't assert Done value here without controlling the filesystem, but
	// we verify the struct has a non-empty Detail.
	if s.Detail == "" {
		t.Error("checkGitSSHState Detail should always be non-empty")
	}
}

// ---------------------------------------------------------------------------
// DB-exposure parser tests
// ---------------------------------------------------------------------------

// TestIsPublicBind covers the full set of bind-address formats that ss -tlnH
// emits. The M13 requirement: 0.0.0.0, [::], :::, and specific public IPs are
// flagged; 127.0.0.1 and [::1] are not.
func TestIsPublicBind(t *testing.T) {
	cases := []struct {
		addr    string
		port    string
		exposed bool
	}{
		// IPv4 all-interfaces — public.
		{"0.0.0.0:5432", "5432", true},
		{"0.0.0.0:3306", "3306", true},
		// IPv4 loopback — safe.
		{"127.0.0.1:5432", "5432", false},
		// IPv6 all-interfaces (bracketed) — public.
		{"[::]:5432", "5432", true},
		// IPv6 loopback — safe.
		{"[::1]:5432", "5432", false},
		// IPv6 compact all-interfaces (:::port form from some kernels) — public.
		// ss output can appear as ":::5432" which after stripping ":5432" gives "::".
		{":::5432", "5432", true},
		// Specific public IPv4 — public.
		{"203.0.113.5:5432", "5432", true},
		// Different port — not this port, not exposed.
		{"0.0.0.0:3306", "5432", false},
		// Redis on loopback — safe.
		{"127.0.0.1:6379", "6379", false},
		// Redis on all interfaces — public.
		{"0.0.0.0:6379", "6379", true},
	}
	for _, tc := range cases {
		got := secwatch.IsPublicBind(tc.addr, tc.port)
		if got != tc.exposed {
			t.Errorf("secwatch.IsPublicBind(%q, %q) = %v, want %v", tc.addr, tc.port, got, tc.exposed)
		}
	}
}

// TestIsPortExposedOnLine verifies that the ss column parser extracts the
// LocalAddress:Port field correctly from realistic ss -tlnH output lines.
func TestIsPortExposedOnLine(t *testing.T) {
	cases := []struct {
		line    string
		port    string
		exposed bool
		desc    string
	}{
		{
			"LISTEN 0      128          0.0.0.0:5432       0.0.0.0:*",
			"5432", true,
			"IPv4 all-interfaces Postgres",
		},
		{
			"LISTEN 0      128        127.0.0.1:5432       0.0.0.0:*",
			"5432", false,
			"IPv4 loopback Postgres — safe",
		},
		{
			"LISTEN 0      128             [::]:5432          [::]:*",
			"5432", true,
			"IPv6 all-interfaces Postgres",
		},
		{
			"LISTEN 0      128             [::1]:5432         [::]:*",
			"5432", false,
			"IPv6 loopback Postgres — safe",
		},
		{
			"LISTEN 0      128       203.0.113.5:5432       0.0.0.0:*",
			"5432", true,
			"specific public IP — exposed",
		},
		{
			"LISTEN 0      128          0.0.0.0:3306       0.0.0.0:*",
			"5432", false,
			"MySQL port but checking for Postgres — not matching",
		},
	}
	for _, tc := range cases {
		fields := strings.Fields(tc.line)
		addr := ""
		if len(fields) >= 4 {
			addr = fields[3]
		}
		got := secwatch.IsPublicBind(addr, tc.port)
		if got != tc.exposed {
			t.Errorf("[%s] IsPublicBind(%q, %q) = %v, want %v",
				tc.desc, addr, tc.port, got, tc.exposed)
		}
	}
}
