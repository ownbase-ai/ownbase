// Package vmhost drives the Multipass CLI to provision and manage local
// development VMs. It is the Go replacement for testing/smoke-install.sh and
// `make connect-vm`: everything that used to shell out to multipass from
// bash now lives here, behind a Runner seam so Tier-1 tests never touch a
// real VM.
//
// Multipass is the locked local-VM backend (docs/decisions.md): it works on
// both macOS and Linux hosts, which is what makes the local-VM setup path in
// `ownbasectl create` (no --remote) first-class alongside remote servers.
package vmhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes one `multipass <args...>` invocation and returns its
// stdout. Production code uses execRunner; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// execRunner is the production Runner: shells out to the real multipass binary.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "multipass", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("multipass %s: %w\n%s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Multipass wraps the multipass CLI. The zero value is not usable; use New.
type Multipass struct {
	Runner Runner
}

// New returns a Multipass driven by the real multipass binary on PATH.
func New() *Multipass {
	return &Multipass{Runner: execRunner{}}
}

// LaunchOptions configures a new VM. Zero values are replaced by sensible
// defaults in Launch (see withDefaults).
type LaunchOptions struct {
	// Image is the Ubuntu image alias (default "24.04").
	Image string
	// CPUs is the number of virtual CPUs (default 2).
	CPUs int
	// MemoryGB is the memory allocation in gigabytes (default 2).
	MemoryGB int
	// DiskGB is the disk allocation in gigabytes (default 15).
	DiskGB int
	// CloudInitPath, when set, is passed as --cloud-init to seed the VM.
	// Leave empty for a plain Ubuntu image (the OwnBase installer bootstraps
	// everything it needs itself — this is the whole point of the M5 design).
	CloudInitPath string
}

func (o LaunchOptions) withDefaults() LaunchOptions {
	if o.Image == "" {
		o.Image = "24.04"
	}
	if o.CPUs == 0 {
		o.CPUs = 2
	}
	if o.MemoryGB == 0 {
		o.MemoryGB = 2
	}
	if o.DiskGB == 0 {
		o.DiskGB = 15
	}
	return o
}

// Launch creates a new VM named name. It does not delete an existing VM with
// the same name — callers that want a fresh VM should call Delete first.
// multipass launch blocks until the instance is created; callers that need
// SSH/transfer should still WaitUntilRunning — a killed launch can leave the
// VM in Starting, and transfer refuses anything but Running.
func (m *Multipass) Launch(ctx context.Context, name string, opts LaunchOptions) error {
	opts = opts.withDefaults()
	args := []string{
		"launch", opts.Image,
		"--name", name,
		"--cpus", strconv.Itoa(opts.CPUs),
		"--memory", fmt.Sprintf("%dG", opts.MemoryGB),
		"--disk", fmt.Sprintf("%dG", opts.DiskGB),
	}
	if opts.CloudInitPath != "" {
		args = append(args, "--cloud-init", opts.CloudInitPath)
	}
	_, err := m.Runner.Run(ctx, args...)
	return err
}

// Delete deletes and purges the named VM. Missing instances are success —
// multipass returns exit 2 for "does not exist", and callers often delete
// defensively after a raced Exists check or a cancelled prior create.
func (m *Multipass) Delete(ctx context.Context, name string) error {
	_, err := m.Runner.Run(ctx, "delete", "--purge", name)
	if err != nil && isMultipassMissing(err) {
		return nil
	}
	return err
}

// DeleteIfExists deletes and purges the named VM only if it currently exists.
// Safe to call unconditionally before Launch to guarantee a fresh VM.
func (m *Multipass) DeleteIfExists(ctx context.Context, name string) error {
	exists, err := m.Exists(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return m.Delete(ctx, name)
}

// isMultipassMissing reports whether err is multipass saying the instance is
// already gone. Matched on stderr text because multipass uses exit 2 for
// several failures, not only this one.
func isMultipassMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "not found")
}

// VMSummary is one entry from `multipass list`.
type VMSummary struct {
	Name  string
	State string
}

type multipassListResponse struct {
	List []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"list"`
}

// List returns all VMs known to multipass (any state).
func (m *Multipass) List(ctx context.Context) ([]VMSummary, error) {
	out, err := m.Runner.Run(ctx, "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("multipass list: %w", err)
	}
	var resp multipassListResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse multipass list output: %w", err)
	}
	summaries := make([]VMSummary, 0, len(resp.List))
	for _, v := range resp.List {
		summaries = append(summaries, VMSummary{Name: v.Name, State: v.State})
	}
	return summaries, nil
}

// Exists returns true when a VM with the given name is known to multipass,
// regardless of its running state.
func (m *Multipass) Exists(ctx context.Context, name string) (bool, error) {
	vms, err := m.List(ctx)
	if err != nil {
		return false, err
	}
	for _, v := range vms {
		if v.Name == name {
			return true, nil
		}
	}
	return false, nil
}

type multipassInfoResponse struct {
	Info map[string]struct {
		IPv4  []string `json:"ipv4"`
		State string   `json:"state"`
	} `json:"info"`
}

// IPv4 returns the primary IPv4 address of the named VM.
func (m *Multipass) IPv4(ctx context.Context, name string) (string, error) {
	out, err := m.Runner.Run(ctx, "info", name, "--format", "json")
	if err != nil {
		return "", fmt.Errorf("multipass info %s: %w", name, err)
	}
	var resp multipassInfoResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse multipass info output: %w", err)
	}
	info, ok := resp.Info[name]
	if !ok || len(info.IPv4) == 0 {
		return "", fmt.Errorf("no IPv4 address found for VM %q (is it running?)", name)
	}
	return info.IPv4[0], nil
}

// State returns the current run state of the named VM (e.g. "Running",
// "Stopped").
func (m *Multipass) State(ctx context.Context, name string) (string, error) {
	out, err := m.Runner.Run(ctx, "info", name, "--format", "json")
	if err != nil {
		return "", fmt.Errorf("multipass info %s: %w", name, err)
	}
	var resp multipassInfoResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse multipass info output: %w", err)
	}
	info, ok := resp.Info[name]
	if !ok {
		return "", fmt.Errorf("VM %q not found", name)
	}
	return info.State, nil
}

// Exec runs a command inside the named VM (as the default VM user) and
// returns its combined stdout.
func (m *Multipass) Exec(ctx context.Context, name string, command ...string) (string, error) {
	args := append([]string{"exec", name, "--"}, command...)
	return m.Runner.Run(ctx, args...)
}

// RunSudoScript runs scriptPath inside the VM as root, with each key/value in
// env exported before the script runs. scriptPath must already exist inside
// the VM (transfer it first with Transfer). Values are single-quoted so keys
// containing spaces (e.g. an SSH public key) are passed through intact.
func (m *Multipass) RunSudoScript(ctx context.Context, name, scriptPath string, env map[string]string) (string, error) {
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%s ", k, shellQuote(v))
	}
	fmt.Fprintf(&b, "bash %s", scriptPath)
	return m.Exec(ctx, name, "sudo", "bash", "-c", b.String())
}

// Transfer copies a local file into the VM at remotePath. The VM must be
// Running; call WaitUntilRunning after Launch when the previous create may
// have been interrupted.
func (m *Multipass) Transfer(ctx context.Context, localPath, name, remotePath string) error {
	_, err := m.Runner.Run(ctx, "transfer", localPath, name+":"+remotePath)
	return err
}

// WaitUntilRunning polls until the named VM reports state Running or ctx is
// done. timeout caps how long to wait when ctx has no deadline.
func (m *Multipass) WaitUntilRunning(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	var last string
	for {
		state, err := m.State(ctx, name)
		if err == nil && strings.EqualFold(state, "Running") {
			return nil
		}
		if err == nil {
			last = state
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			if last == "" {
				return fmt.Errorf("timed out waiting for VM %q to reach Running", name)
			}
			return fmt.Errorf("timed out waiting for VM %q to reach Running (last: %s)", name, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes,
// so it can be safely interpolated into a `sh -c` command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
