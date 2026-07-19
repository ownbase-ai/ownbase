package install

import (
	"context"
	"strings"
)

// ensureGit installs git if it is not already present. Git is required by the
// daemon to clone the external config repo (see internal/configsource) and
// every service's repo: (the applier clones the local bare clone at the pinned
// ref before podman build). Ubuntu server images — notably the minimal 26.04
// image — do not ship git by default, so a fresh Base crash-loops on bootstrap
// ("exec: git: executable file not found in $PATH") without this step.
func ensureGit(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkGitState(ctx)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install git"}
	}
	s2, err := apt(ctx, "git", false)
	if err != nil {
		return StepStatus{Err: err}
	}
	return s2
}

// checkGitState returns whether git is present without making changes.
func checkGitState(ctx context.Context) StepStatus {
	if !cmdExists("git") {
		return StepStatus{Done: false, Detail: "git not found"}
	}
	out, err := run(ctx, "git", "--version")
	if err != nil {
		return StepStatus{Done: false, Detail: "git --version failed: " + err.Error()}
	}
	return StepStatus{Done: true, AlreadyOK: true, Detail: strings.TrimSpace(out)}
}
