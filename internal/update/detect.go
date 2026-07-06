package update

// detect.go implements local-git helpers for:
//   - resolving a repo's default branch and its HEAD commit SHA
//   - computing how many commits a pinned ref is behind the branch HEAD
//   - finding the newest semver tag on a repo (for drift reporting)
//
// All of these operate on the local bare repo under /opt/ownbase/repos/
// (see internal/repos) — no network access or API token required. The
// external git host, if any, is only consulted by internal/repos when a
// service is first added or its ref: is updated to something not yet fetched.

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Commit-SHA detection
// ---------------------------------------------------------------------------

// isCommitSHA reports whether ref is a full 40-character hex commit SHA,
// as opposed to a branch or tag name. A full SHA is already maximally
// pinned — there is nothing more specific to compare it against — so
// ComputeDrift skips the tag-based drift comparison for these refs.
func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Default branch + HEAD resolution
// ---------------------------------------------------------------------------

// resolveDefaultBranchHead returns the default branch name and its HEAD
// commit SHA for the local bare repo backing sourcePath.
// Returns ("", "", nil) when the repo doesn't exist locally or has no
// commits yet.
func resolveDefaultBranchHead(_ context.Context, cfg Config, sourcePath string) (branch, sha string, err error) {
	repoPath := cfg.repoPath(sourcePath)

	branchOut, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		// Repo missing, not yet a git dir, or HEAD is unborn (no commits).
		return "", "", nil
	}
	branch = strings.TrimSpace(string(branchOut))
	if branch == "" {
		branch = "main"
	}

	shaOut, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		// Branch exists in HEAD but has no commits yet.
		return branch, "", nil
	}
	return branch, strings.TrimSpace(string(shaOut)), nil
}

// ---------------------------------------------------------------------------
// Commits-behind calculation
// ---------------------------------------------------------------------------

// commitsBehind returns how many commits `base` is behind `head` in the
// local bare repo backing sourcePath.
func commitsBehind(_ context.Context, cfg Config, sourcePath, base, head string) (int, error) {
	if base == head {
		return 0, nil
	}
	repoPath := cfg.repoPath(sourcePath)

	out, err := exec.Command("git", "-C", repoPath, "rev-list", base+".."+head, "--count").Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..%s in %s: %w", base, head, repoPath, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list count %q: %w", string(out), err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Newest tag detection (for drift reporting)
// ---------------------------------------------------------------------------

// fetchLatestSourceRef returns the name of the newest semver tag in the
// local bare repo backing sourcePath. Returns "" (no error) when the repo
// has no tags or doesn't exist locally.
func fetchLatestSourceRef(_ context.Context, cfg Config, sourcePath string) (string, error) {
	repoPath := cfg.repoPath(sourcePath)

	out, err := exec.Command("git", "-C", repoPath, "tag", "--list").Output()
	if err != nil {
		return "", nil // repo missing or not a git dir — no tags to report
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	return highestVersionTag(names), nil
}

// ---------------------------------------------------------------------------
// Semver-aware tag sorting
// ---------------------------------------------------------------------------

// HighestVersionTag returns the tag from names that represents the highest
// version, using a semver-aware comparator. Returns "" when names is empty.
func HighestVersionTag(names []string) string {
	return highestVersionTag(names)
}

func highestVersionTag(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Slice(sorted, func(i, j int) bool {
		return compareVersionTags(sorted[i], sorted[j]) < 0
	})
	return sorted[len(sorted)-1]
}

// compareVersionTags returns -1, 0, or +1 for a semver-aware tag comparison.
// Strips a leading "v" then compares dot-separated numeric segments as integers.
// Falls back to lexicographic comparison for non-numeric segments.
func compareVersionTags(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	n := len(partsA)
	if len(partsB) < n {
		n = len(partsB)
	}
	for i := 0; i < n; i++ {
		na, errA := strconv.Atoi(partsA[i])
		nb, errB := strconv.Atoi(partsB[i])
		if errA == nil && errB == nil {
			if na < nb {
				return -1
			}
			if na > nb {
				return 1
			}
		} else {
			if partsA[i] < partsB[i] {
				return -1
			}
			if partsA[i] > partsB[i] {
				return 1
			}
		}
	}
	if len(partsA) < len(partsB) {
		return -1
	}
	if len(partsA) > len(partsB) {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Image ref parsing (retained for core-package digest detection)
// ---------------------------------------------------------------------------

// ParseImageRef splits "registry/repo:tag" into its components.
// Handles references like "docker.io/library/caddy:2" and
// "docker.io/library/nginx:latest". Exported for testing and for the M12
// core-package update detection path (Tier-2).
func ParseImageRef(image string) (registry, repo, tag string, err error) {
	at := strings.LastIndex(image, "@")
	if at >= 0 {
		return "", "", "", fmt.Errorf("image ref %q already contains a digest", image)
	}

	colonIdx := strings.LastIndex(image, ":")
	if colonIdx < 0 {
		tag = "latest"
	} else {
		tag = image[colonIdx+1:]
		image = image[:colonIdx]
	}

	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 1 {
		return "registry-1.docker.io", "library/" + parts[0], tag, nil
	}

	firstPart := parts[0]
	if strings.ContainsAny(firstPart, ".:") || firstPart == "localhost" {
		return firstPart, parts[1], tag, nil
	}

	return "registry-1.docker.io", image, tag, nil
}
