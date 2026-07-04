package update

// detect.go implements Forgejo API helpers for:
//   - resolving a repo's default branch and its HEAD commit SHA
//   - computing how many commits a pinned ref is behind the branch HEAD
//   - finding the newest semver tag on a repo (for drift reporting)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Default branch + HEAD resolution
// ---------------------------------------------------------------------------

// resolveDefaultBranchHead returns the default branch name and its HEAD commit
// SHA for sourcePath in the local Forgejo instance.
// Returns ("", "", nil) when the repo exists but has no commits.
func resolveDefaultBranchHead(ctx context.Context, cfg Config, sourcePath string) (branch, sha string, err error) {
	owner, repo := parseSourcePath(sourcePath, cfg.ForgejoUser)

	// Step 1: get the repo's default_branch name.
	repoURL := fmt.Sprintf("%s/api/v1/repos/%s/%s", cfg.ForgejoURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build repo request for %s/%s: %w", owner, repo, err)
	}
	req.Header.Set("Authorization", "token "+cfg.ForgejoToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GET repo %s/%s: %w", owner, repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET repo %s/%s: status %d", owner, repo, resp.StatusCode)
	}

	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repoInfo); err != nil {
		return "", "", fmt.Errorf("decode repo %s/%s: %w", owner, repo, err)
	}
	if repoInfo.DefaultBranch == "" {
		repoInfo.DefaultBranch = "main"
	}

	// Step 2: get the branch's HEAD commit SHA.
	branchURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/branches/%s",
		cfg.ForgejoURL, owner, repo, repoInfo.DefaultBranch)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, branchURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build branch request for %s/%s/%s: %w", owner, repo, repoInfo.DefaultBranch, err)
	}
	req2.Header.Set("Authorization", "token "+cfg.ForgejoToken)

	resp2, err := client.Do(req2)
	if err != nil {
		return "", "", fmt.Errorf("GET branch %s/%s/%s: %w", owner, repo, repoInfo.DefaultBranch, err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusNotFound {
		// Branch exists in repo info but has no commits yet.
		return repoInfo.DefaultBranch, "", nil
	}
	if resp2.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET branch %s/%s/%s: status %d", owner, repo, repoInfo.DefaultBranch, resp2.StatusCode)
	}

	var branchInfo struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&branchInfo); err != nil {
		return "", "", fmt.Errorf("decode branch %s/%s/%s: %w", owner, repo, repoInfo.DefaultBranch, err)
	}

	return repoInfo.DefaultBranch, branchInfo.Commit.ID, nil
}

// ---------------------------------------------------------------------------
// Commits-behind calculation
// ---------------------------------------------------------------------------

// commitsBehind returns how many commits `base` is behind `head` in the given
// repo via the Forgejo compare API. Returns 0 on any transient error so the
// caller can still produce a partial result.
func commitsBehind(ctx context.Context, cfg Config, sourcePath, base, head string) (int, error) {
	if base == head {
		return 0, nil
	}
	owner, repo := parseSourcePath(sourcePath, cfg.ForgejoUser)

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/compare/%s...%s",
		cfg.ForgejoURL, owner, repo, base, head)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build compare request: %w", err)
	}
	req.Header.Set("Authorization", "token "+cfg.ForgejoToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET compare %s/%s %s...%s: %w", owner, repo, base, head, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GET compare %s/%s %s...%s: status %d", owner, repo, base, head, resp.StatusCode)
	}

	var result struct {
		TotalCommits int `json:"total_commits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode compare %s/%s: %w", owner, repo, err)
	}
	return result.TotalCommits, nil
}

// ---------------------------------------------------------------------------
// Newest tag detection (for drift reporting)
// ---------------------------------------------------------------------------

// fetchLatestSourceRef returns the name of the newest semver tag for the repo
// at sourcePath in the local Forgejo instance. Returns "" (no error) when the
// repo has no tags or is not found.
func fetchLatestSourceRef(ctx context.Context, cfg Config, sourcePath string) (string, error) {
	owner, repo := parseSourcePath(sourcePath, cfg.ForgejoUser)

	const tagsPageSize = 50
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/tags?limit=%d&page=1",
		cfg.ForgejoURL, owner, repo, tagsPageSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build tag request for %s/%s: %w", owner, repo, err)
	}
	req.Header.Set("Authorization", "token "+cfg.ForgejoToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET tags %s/%s: %w", owner, repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET tags %s/%s: status %d", owner, repo, resp.StatusCode)
	}

	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return "", fmt.Errorf("decode tags %s/%s: %w", owner, repo, err)
	}
	if len(tags) == 0 {
		return "", nil
	}

	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return highestVersionTag(names), nil
}

// ---------------------------------------------------------------------------
// Forgejo source path parsing
// ---------------------------------------------------------------------------

// parseSourcePath splits a Forgejo repo path (e.g. "services/auth") into
// owner and repo. If sourcePath has no slash, defaultOwner is used as owner.
// Exported so podman.go can reuse the same logic without importing this package.
func parseSourcePath(sourcePath, defaultOwner string) (owner, repo string) {
	parts := strings.SplitN(sourcePath, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return defaultOwner, sourcePath
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
// Handles references like "codeberg.org/forgejo/forgejo:10" and
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
