package main

// confirm.go implements the y/N confirmation prompt used by destructive
// commands (delete, the VM overwrite in create/restore).

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// confirm asks the user to approve a destructive action with a y/N prompt.
//
// The prompt is TTY-only: when stdin is not a terminal (scripts, CI, tests),
// the action proceeds as if confirmed — non-interactive callers keep the
// pre-prompt behavior and are expected to pass --yes to be explicit.
// assumeYes (--yes/-y) skips the prompt unconditionally.
func confirm(prompt string, assumeYes bool) bool {
	if assumeYes || !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Printf("%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// errAborted is returned when the user declines a confirmation prompt.
var errAborted = fmt.Errorf("aborted")
