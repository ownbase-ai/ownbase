package explain

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// kernelPkgPrefixes are versioned kernel package name prefixes. The suffix
// after the prefix is the ABI (e.g. "7.0.0-28-generic"). Metapackages like
// linux-image-generic have no numeric ABI and are left alone.
// Longer prefixes first so "linux-modules-extra-" wins over "linux-modules-".
var kernelPkgPrefixes = []string{
	"linux-main-modules-zfs-",
	"linux-modules-extra-",
	"linux-modules-",
	"linux-headers-",
	"linux-image-",
}

// obsoleteKernelPackages lists installed versioned kernel packages whose ABI
// is not the newest installed one. apt-get upgrade --with-new-pkgs installs a
// new linux-image-N package but leaves the previous ABI installed; trivy then
// keeps reporting every CVE on the old modules as fixable. Purging everything
// except the newest ABI clears those findings (a reboot is still required to
// actually run the new kernel).
func obsoleteKernelPackages(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f", "${Package} ${Status}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}

	// abi → package names for that ABI
	byABI := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		pkg := fields[0]
		// Status is "install ok installed" for live packages.
		if fields[len(fields)-1] != "installed" {
			continue
		}
		abi, ok := kernelABI(pkg)
		if !ok {
			continue
		}
		byABI[abi] = append(byABI[abi], pkg)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(byABI) <= 1 {
		return nil, nil
	}

	abis := make([]string, 0, len(byABI))
	for abi := range byABI {
		abis = append(abis, abi)
	}
	sort.Slice(abis, func(i, j int) bool {
		return kernelABILess(ctx, abis[i], abis[j])
	})
	newest := abis[len(abis)-1]

	var obsolete []string
	for abi, pkgs := range byABI {
		if abi == newest {
			continue
		}
		obsolete = append(obsolete, pkgs...)
	}
	sort.Strings(obsolete)
	return obsolete, nil
}

// kernelABI returns the ABI suffix of a versioned kernel package, or false
// for metapackages (linux-image-generic) and unrelated names.
func kernelABI(pkg string) (string, bool) {
	for _, prefix := range kernelPkgPrefixes {
		if !strings.HasPrefix(pkg, prefix) {
			continue
		}
		rest := strings.TrimPrefix(pkg, prefix)
		// Versioned ABIs start with a digit ("7.0.0-28-generic"). Metapackages
		// use words ("generic", "virtual", "aws").
		if rest == "" || rest[0] < '0' || rest[0] > '9' {
			return "", false
		}
		return rest, true
	}
	return "", false
}

// kernelABILess reports whether a is an older kernel ABI than b, using dpkg's
// version comparator when available.
func kernelABILess(ctx context.Context, a, b string) bool {
	cmd := exec.CommandContext(ctx, "dpkg", "--compare-versions", a, "lt", b)
	if err := cmd.Run(); err == nil {
		return true
	}
	// dpkg returns exit 1 when the relation is false; any other failure falls
	// back to a naive string compare so tests without dpkg still work.
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 {
		return false
	}
	return a < b
}
