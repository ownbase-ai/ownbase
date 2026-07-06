package install

// notify_sudoers.go grants the admin SSH user permission to wake the
// daemon immediately after a direct git push, without granting any
// broader sudo access.
//
// The post-receive hook (see internal/githost.HookScript) runs as
// whichever user pushed — the admin user, for a direct `git push` over
// SSH — and needs to signal the daemon (which runs as root) to reconcile
// right away. Sending a signal requires either a matching UID or root, so
// without this, a hook-triggered push from a non-root admin user (e.g.
// "ubuntu" on a local VM) silently fails to wake the daemon and the
// change waits for the periodic timer backstop instead of applying
// immediately.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// NotifySudoersPath is the sudoers drop-in granting the admin user
// passwordless permission to run exactly one fixed command: signaling the
// daemon via its PID file. No argument is attacker-controlled — the
// pidfile path is hardcoded in the grant itself — so this cannot be used
// to run anything other than "wake the daemon".
const NotifySudoersPath = "/etc/sudoers.d/ownbase-notify"

// EnsureNotifySudoers writes NotifySudoersPath granting adminUser
// passwordless sudo to run `pkill -USR1 -F /opt/ownbase/daemon.pid` (see
// githost.HookScript). A no-op when adminUser is empty or "root" — root
// already has full sudo rights via the stock sudoers file and does not
// need a dedicated grant.
//
// The generated content is validated with `visudo -c` before being
// installed, so a malformed grant can never disable sudo for the whole
// machine. Idempotent: overwrites the file with identical content on
// every call; safe to call on every daemon start.
func EnsureNotifySudoers(adminUser string) error {
	if adminUser == "" || adminUser == "root" {
		return nil
	}
	return writeSudoersFile(NotifySudoersPath, adminUser)
}

// writeSudoersFile implements EnsureNotifySudoers against an arbitrary
// destination path, kept separate so tests can target a temp file instead
// of the real /etc/sudoers.d/ownbase-notify.
func writeSudoersFile(destPath, adminUser string) error {
	content := fmt.Sprintf(
		"# Managed by ownbased — do not edit by hand (regenerated on every start).\n"+
			"# Lets the post-receive hook (running as %s after a direct git push)\n"+
			"# wake the daemon immediately instead of waiting for the timer backstop.\n"+
			"%s ALL=(root) NOPASSWD: /usr/bin/pkill -USR1 -F /opt/ownbase/daemon.pid\n",
		adminUser, adminUser,
	)

	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".ownbase-notify-*")
	if err != nil {
		return fmt.Errorf("create temp sudoers file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once successfully renamed into place

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp sudoers file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp sudoers file: %w", err)
	}
	// sudo refuses to honor a sudoers file that is world- or group-writable.
	if err := os.Chmod(tmpPath, 0o440); err != nil {
		return fmt.Errorf("chmod temp sudoers file: %w", err)
	}

	if out, err := exec.Command("visudo", "-cf", tmpPath).CombinedOutput(); err != nil {
		return fmt.Errorf("invalid sudoers content (not installed): %w\n%s", err, out)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("install %s: %w", destPath, err)
	}
	return nil
}
