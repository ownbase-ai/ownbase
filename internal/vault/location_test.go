package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ExpandTilde must resolve a leading ~ into the real home directory, not drop
// it — the documented `vault init ~/Dropbox/OwnBase` and `--import
// ~/.ssh/...` flows depend on this exactly.
func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available in this environment: %v", err)
	}

	cases := []struct {
		name string
		path string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash subpath", "~/Dropbox/OwnBase", filepath.Join(home, "Dropbox", "OwnBase")},
		{"tilde slash dotfile", "~/.ssh/id_ed25519", filepath.Join(home, ".ssh", "id_ed25519")},
		{"no tilde stays untouched", "/already/absolute", "/already/absolute"},
		{"relative path stays untouched", "relative/path", "relative/path"},
		{"empty string stays empty", "", ""},
		{
			"tilde embedded mid-path is not special",
			"/Users/x/com~apple~CloudDocs/OwnBase",
			"/Users/x/com~apple~CloudDocs/OwnBase",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExpandTilde(c.path)
			if got != c.want {
				t.Errorf("ExpandTilde(%q) = %q, want %q", c.path, got, c.want)
			}
			// The failure mode this guards against: a leading ~/ resolving
			// under root instead of home, because filepath.Join treated the
			// remainder as an absolute path segment that discards whatever
			// came before it.
			if len(c.path) > 0 && c.path[0] == '~' && !strings.HasPrefix(got, home) {
				t.Errorf("ExpandTilde(%q) = %q escaped the home directory %q", c.path, got, home)
			}
		})
	}
}
