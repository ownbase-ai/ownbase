package secwatch

import (
	"os"
	"strings"
)

// rebootRequiredPath is where Ubuntu/Debian leave a marker after an apt
// upgrade that needs a reboot to take effect (typically a new kernel). The
// companion .pkgs file lists which packages triggered it.
const (
	rebootRequiredPath     = "/var/run/reboot-required"
	rebootRequiredPkgsPath = "/var/run/reboot-required.pkgs"
)

// RebootResult answers whether the host needs a reboot for applied packages
// to take effect. Without this, a post-upgrade trivy scan can report clean
// while the machine is still running a vulnerable kernel.
//
// The probe is a pair of file stats — no shell-out — so it is safe to run on
// every secwatch tick and degrades to Required=false off-Linux.
type RebootResult struct {
	// Required is true when /var/run/reboot-required exists.
	Required bool `json:"required"`
	// Packages lists the basenames from /var/run/reboot-required.pkgs when
	// that file is present. Empty when Required is false or the pkgs file
	// is missing.
	Packages []string `json:"packages,omitempty"`
}

// GatherRebootRequired reports whether the host needs a reboot.
//
// Always succeeds: a missing marker file means Required=false, not an error.
// That is the honest answer on macOS and on a Base that has never upgraded a
// reboot-sensitive package.
func GatherRebootRequired() RebootResult {
	return gatherRebootAt(rebootRequiredPath, rebootRequiredPkgsPath)
}

// gatherRebootAt is the path-parameterised core of GatherRebootRequired so
// Tier-1 tests can exercise the positive case without root under /var/run.
func gatherRebootAt(marker, pkgs string) RebootResult {
	if _, err := os.Stat(marker); err != nil {
		return RebootResult{}
	}
	out := RebootResult{Required: true}
	data, err := os.ReadFile(pkgs)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		pkg := strings.TrimSpace(line)
		if pkg == "" {
			continue
		}
		out.Packages = append(out.Packages, pkg)
	}
	return out
}
