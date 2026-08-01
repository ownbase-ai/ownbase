package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StateDir is the directory holding OwnBase's non-secret client state: the
// pointer to the vault, the agent sockets, and known_hosts.
const StateDir = ".ownbase"

// PointerFile records where the vault lives. It holds a path, not a secret —
// the vault itself can sit in iCloud, Dropbox, a syncthing folder, or an
// external disk, and this is how every ownbasectl invocation finds it again.
const PointerFile = "vault"

// PathEnv overrides the recorded vault location for one invocation. Useful for
// tests and for a second vault kept on removable media.
const PathEnv = "OWNBASE_VAULT"

// DefaultFileName is the vault filename suggested when the user gives a
// directory rather than a file.
const DefaultFileName = "ownbase.kdbx"

// ErrNoVault reports that no vault location has been recorded yet.
var ErrNoVault = errors.New("no vault configured")

// StatePath returns a path inside ~/.ownbase.
func StatePath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, StateDir, name), nil
}

// ResolvePath returns the configured vault location, from $OWNBASE_VAULT if
// set, otherwise from the pointer file.
func ResolvePath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return ExpandTilde(p), nil
	}
	pointer, err := StatePath(PointerFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(pointer)
	if os.IsNotExist(err) {
		return "", ErrNoVault
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", pointer, err)
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return "", ErrNoVault
	}
	return ExpandTilde(path), nil
}

// NormalizePath resolves a user-supplied vault location the same way
// RecordPath does, without writing the pointer file.
//
// A path that names a directory — or that could only be one, having no file
// extension — gets DefaultFileName appended. Without the second half of that,
// `vault init ~/Dropbox/OwnBase` on a folder that does not exist yet would
// silently create a *file* called `OwnBase`, and the user would find no folder
// where they asked for one.
//
// vault init uses this first and only calls RecordPath after create/unlock
// succeeds, so a cancelled password prompt or a wrong password cannot move
// ~/.ownbase/vault off a previously working location.
func NormalizePath(path string) (string, error) {
	path = ExpandTilde(strings.TrimSpace(path))
	if path == "" {
		return "", errors.New("a vault path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, statErr := os.Stat(abs)
	switch {
	case statErr == nil && info.IsDir():
		abs = filepath.Join(abs, DefaultFileName)
	case os.IsNotExist(statErr) && filepath.Ext(abs) == "":
		abs = filepath.Join(abs, DefaultFileName)
	}
	return abs, nil
}

// RecordPath writes the pointer file so later invocations find the vault,
// returning the resolved vault file path.
func RecordPath(path string) (string, error) {
	abs, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	if err := writePointer(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// writePointer records abs as the vault location in ~/.ownbase/vault.
func writePointer(abs string) error {
	pointer, err := StatePath(PointerFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(pointer), err)
	}
	if err := os.WriteFile(pointer, []byte(abs+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", pointer, err)
	}
	return nil
}

// ExpandTilde replaces a leading ~ with the user's home directory.
func ExpandTilde(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
