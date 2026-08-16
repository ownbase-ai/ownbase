// Package selfupdate downloads a newer ownbased binary from the OwnBase
// release server, verifies its minisign signature, and atomically replaces
// the running binary. Systemd's Restart=always then boots the new process.
//
// The release server, signing key, and stage-and-rename install path match
// install.sh so a self-update and a fresh install are the same artifact.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MinisignPublicKey is the OwnBase release signing key. Same value as
// install.sh's MINISIGN_PUBLIC_KEY — generated once, never rotates without a
// coordinated OwnBase major.
const MinisignPublicKey = "RWTaLp3BlckCjjicEDrN7oVrRhGDWhSjgOpR2Ue/yHzP0cFsmmxALr/V"

// DefaultReleaseBaseURL is the S3/R2 prefix that holds signed daemon binaries.
const DefaultReleaseBaseURL = "https://releases.ownbase.ai/daemon"

// OriginEnv is the release origin (no /daemon suffix). Same name as
// internal/release.BaseURLEnv and the version-check client. When set,
// binaries are fetched from $OWNBASE_RELEASE_URL/daemon/…
const OriginEnv = "OWNBASE_RELEASE_URL"

// DaemonBaseEnv is the full daemon artifact prefix (includes /daemon).
// Same name as install.sh's RELEASE_BASE_URL so a fork sets one knob for
// install and self-update.
const DaemonBaseEnv = "RELEASE_BASE_URL"

// DefaultBinaryPath is where install.sh places the daemon.
const DefaultBinaryPath = "/opt/ownbase/bin/ownbased"

// Options control a self-update run.
type Options struct {
	// Version is the target release tag (e.g. "v0.4.0") or "latest".
	Version string
	// ReleaseBaseURL overrides the env-resolved daemon prefix.
	ReleaseBaseURL string
	// BinaryPath is the live daemon path to replace. Empty → DefaultBinaryPath.
	BinaryPath string
	// SkipVerify disables minisign (tests only).
	SkipVerify bool
	// CurrentVersion is the version of the running binary. When equal to the
	// resolved target (and target is not "latest"), Apply is a no-op.
	CurrentVersion string
	// Writer receives progress lines. Optional.
	Writer io.Writer
}

// Result is what Apply returns after a successful swap (or a no-op).
type Result struct {
	// Updated is false when the running binary is already the requested version.
	Updated bool `json:"updated"`
	// From is the version that was running.
	From string `json:"from,omitempty"`
	// To is the version now on disk (or already running).
	To string `json:"to"`
	// RestartPending is true when the binary was swapped and the process
	// should exit so systemd restarts it into the new inode.
	RestartPending bool `json:"restart_pending,omitempty"`
}

func (o Options) releaseBase() string {
	if o.ReleaseBaseURL != "" {
		return strings.TrimRight(o.ReleaseBaseURL, "/")
	}
	// RELEASE_BASE_URL is the full …/daemon prefix (install.sh).
	if v := strings.TrimSpace(os.Getenv(DaemonBaseEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	// OWNBASE_RELEASE_URL is the origin; binaries live under /daemon.
	if v := strings.TrimSpace(os.Getenv(OriginEnv)); v != "" {
		return strings.TrimRight(v, "/") + "/daemon"
	}
	return DefaultReleaseBaseURL
}

func (o Options) binaryPath() string {
	if o.BinaryPath != "" {
		return o.BinaryPath
	}
	return DefaultBinaryPath
}

func (o Options) logf(format string, args ...any) {
	if o.Writer == nil {
		return
	}
	fmt.Fprintf(o.Writer, format+"\n", args...)
}

// Arch maps GOARCH to the release asset suffix (amd64 / arm64).
func Arch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", runtime.GOARCH)
	}
}

// Apply downloads, verifies, and installs the requested version. On success
// with Updated=true the caller must exit so systemd restarts into the new
// binary; Apply itself does not call os.Exit.
func Apply(ctx context.Context, opts Options) (Result, error) {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "latest"
	}
	// A pin that already matches is a no-op — except "latest", which always
	// re-pulls so a Base can catch up without knowing the newest tag.
	if version != "latest" && opts.CurrentVersion != "" && opts.CurrentVersion == version {
		opts.logf("==> Already running %s — nothing to do", version)
		return Result{Updated: false, From: opts.CurrentVersion, To: version}, nil
	}

	arch, err := Arch()
	if err != nil {
		return Result{}, err
	}
	base := opts.releaseBase()
	var url, sigURL string
	if version == "latest" {
		url = fmt.Sprintf("%s/latest/ownbased-linux-%s", base, arch)
	} else {
		url = fmt.Sprintf("%s/%s/ownbased-linux-%s", base, version, arch)
	}
	sigURL = url + ".minisig"

	tmpdir, err := os.MkdirTemp("", "ownbase-selfupdate-*")
	if err != nil {
		return Result{}, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpdir)

	binPath := filepath.Join(tmpdir, "ownbased")
	opts.logf("==> Downloading %s", url)
	if err := download(ctx, url, binPath); err != nil {
		return Result{}, err
	}
	if !opts.SkipVerify {
		opts.logf("==> Verifying minisign signature")
		if err := verify(ctx, binPath, sigURL, tmpdir); err != nil {
			return Result{}, err
		}
	} else {
		opts.logf("==> WARNING: signature verification skipped")
	}
	if err := os.Chmod(binPath, 0o755); err != nil {
		return Result{}, err
	}

	// Best-effort: ask the new binary what it is so the result is honest.
	toVersion := version
	if v, err := readBinaryVersion(ctx, binPath); err == nil && v != "" {
		toVersion = v
		if opts.CurrentVersion != "" && opts.CurrentVersion == v {
			opts.logf("==> Downloaded binary is still %s — already current", v)
			return Result{Updated: false, From: opts.CurrentVersion, To: v}, nil
		}
	}

	dest := opts.binaryPath()
	opts.logf("==> Installing to %s (atomic rename)", dest)
	if err := installBinary(binPath, dest); err != nil {
		return Result{}, err
	}
	opts.logf("==> Installed %s. Process will exit so systemd restarts into the new binary.", toVersion)
	return Result{
		Updated:        true,
		From:           opts.CurrentVersion,
		To:             toVersion,
		RestartPending: true,
	}, nil
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write download: %w", err)
	}
	return nil
}

// verify shells out to minisign, matching install.sh. The Base already has
// minisign after a normal install; if it is missing we try to apt-get it.
func verify(ctx context.Context, binary, sigURL, tmpdir string) error {
	if _, err := exec.LookPath("minisign"); err != nil {
		// Best-effort install — non-fatal until the verify step itself fails.
		_ = exec.CommandContext(ctx, "apt-get", "install", "-y", "-q", "minisign").Run()
		if _, err := exec.LookPath("minisign"); err != nil {
			return fmt.Errorf("minisign not installed and could not be installed: %w", err)
		}
	}
	sigPath := filepath.Join(tmpdir, "ownbased.minisig")
	if err := download(ctx, sigURL, sigPath); err != nil {
		return fmt.Errorf("download signature: %w", err)
	}
	cmd := exec.CommandContext(ctx, "minisign", "-Vm", binary, "-x", sigPath, "-P", MinisignPublicKey)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("signature verification failed: %w\n%s", err, out)
	}
	return nil
}

// installBinary stages next to dest and renames over it. Rename is what makes
// this safe against a running daemon (ETXTBSY on a direct overwrite).
func installBinary(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	staged := dest + ".new"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(staged)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(staged)
		return err
	}
	// Match install.sh ownership when possible; ignore failure off-root.
	_ = exec.Command("chown", "ownbase:ownbase", staged).Run()
	if err := os.Rename(staged, dest); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

func readBinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	// Expect "ownbased v0.4.0" or just "v0.4.0".
	s := strings.TrimSpace(string(out))
	s = strings.TrimPrefix(s, "ownbased ")
	s = strings.TrimSpace(s)
	return s, nil
}
