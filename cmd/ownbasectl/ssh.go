package main

// ssh.go implements `ownbasectl ssh` and `ownbasectl sessions` — the recorded
// way onto a Base, and the way to read back what happened.
//
// Why this replaces plain `ssh`: an agent that can log into your machine as
// root is only as trustworthy as your ability to see what it did afterward.
// Going through ownbasectl buys two things at once. The agent never needs to
// know where a key lives, because the credential agent signs for it. And every
// keystroke and every byte of output lands in an asciicast recording under
// ~/.ownbase/sessions, which the desktop app replays and `sessions show` prints.
//
// Recording cannot be disabled. A switch to turn it off would make the audit
// trail worthless exactly when it matters.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/sshsession"
)

func newSSHCmd() *cobra.Command {
	var (
		command string
		noTTY   bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "ssh <name> [-- <command>...]",
		Short: "Open a recorded shell on a Base, or run one command",
		Long: `Opens an interactive shell on the Base, or runs a single command when one
is given after --. Either way the whole session is recorded to
~/.ownbase/sessions/<base>/ in asciicast v2 format, which 'ownbasectl
sessions show', the OwnBase app, and 'asciinema play' can all replay.

Use this instead of plain ssh. It authenticates with the owner key from your
vault — signed by the credential agent, so no private key is ever handed to
this process — and it is the only path that produces an audit trail.

The command's exit code is the remote command's exit code, so this composes
in scripts the same way ssh does.`,
		Example: `  ownbasectl ssh mybase
  ownbasectl ssh mybase -- systemctl status ownbased
  ownbasectl ssh mybase --command 'journalctl -u ownbased -n 50'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Everything after the Base name is the remote command, so a
			// pipeline or a quoted string both arrive intact.
			if len(args) > 1 {
				if command != "" {
					return withExitCode(exitUsage, fmt.Errorf(
						"give the command either after -- or with --command, not both"))
				}
				command = strings.Join(args[1:], " ")
			}
			return runSSH(args[0], command, noTTY, jsonOut)
		},
	}
	cmd.Flags().StringVar(&command, "command", "", "run this command instead of opening a shell")
	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "do not allocate a terminal on the Base")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the session's recording metadata as JSON when it ends")
	return cmd
}

func runSSH(base, command string, noTTY, jsonOut bool) error {
	target, _, err := baseTarget(base)
	if err != nil {
		return err
	}

	// The banner goes to stderr so `ownbasectl ssh base -- cat file > out`
	// still produces exactly the remote file.
	if command == "" {
		fmt.Fprintf(os.Stderr, "ownbasectl: opening a recorded shell on %s (%s) ...\n", base, target.Destination())
	}

	res, runErr := sshsession.Run(sshsession.Options{
		Base:       base,
		Target:     target,
		Command:    command,
		ForceNoTTY: noTTY,
	})
	if res == nil {
		return runErr
	}

	if jsonOut {
		if err := printJSON(res.Meta); err != nil {
			return err
		}
	} else if command == "" {
		fmt.Fprintf(os.Stderr, "\nownbasectl: session recorded as %s (%s)\n", res.Meta.ID, res.Meta.CastPath)
	}
	if runErr != nil {
		return runErr
	}
	// The remote command's status is the command's result, so pass it through
	// rather than collapsing every non-zero exit into 1.
	if res.ExitCode != 0 {
		return withExitCode(res.ExitCode, fmt.Errorf("remote command exited %d", res.ExitCode))
	}
	return nil
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List and replay recorded ssh sessions",
		Long: `Every shell opened with 'ownbasectl ssh' is recorded. These commands are
how you read the trail back: which sessions ran, when, who opened them, and
exactly what was typed and returned.

Recordings live in ~/.ownbase/sessions/<base>/ as asciicast v2 files. They
are yours: replay them with 'asciinema play', or read the transcript here.`,
	}
	cmd.AddCommand(newSessionsListCmd(), newSessionsShowCmd(), newSessionsPathCmd())
	return cmd
}

func newSessionsListCmd() *cobra.Command {
	var (
		jsonOut bool
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "list [name]",
		Short: "List recorded sessions, newest first (all Bases, or one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := ""
			if len(args) == 1 {
				base = args[0]
			}
			return runSessionsList(base, limit, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the sessions as JSON")
	cmd.Flags().IntVar(&limit, "limit", 50, "show at most this many sessions (0 = all)")
	return cmd
}

func runSessionsList(base string, limit int, jsonOut bool) error {
	sessions, err := sshsession.List(base)
	if err != nil {
		return err
	}
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	if jsonOut {
		if sessions == nil {
			sessions = []sshsession.Meta{}
		}
		return printJSON(sessions)
	}
	if len(sessions) == 0 {
		fmt.Println("No recorded sessions yet.")
		fmt.Println("  Open one with: ownbasectl ssh <name>")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tBASE\tSTARTED\tDURATION\tEXIT\tBY\tCOMMAND")
	for _, m := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			m.ID, m.Base,
			m.StartedAt.Local().Format("Jan 02 15:04"),
			formatSessionDuration(m),
			formatSessionExit(m),
			m.Invoker,
			sessionCommandLabel(m))
	}
	return tw.Flush()
}

func formatSessionDuration(m sshsession.Meta) string {
	if m.EndedAt == nil {
		return "running"
	}
	return m.Duration().Round(time.Second).String()
}

func formatSessionExit(m sshsession.Meta) string {
	switch {
	case m.EndedAt == nil:
		return "-"
	case m.Error != "":
		return "failed"
	case m.ExitCode < 0:
		return "?"
	default:
		return fmt.Sprintf("%d", m.ExitCode)
	}
}

func sessionCommandLabel(m sshsession.Meta) string {
	if m.Command == "" {
		return "(interactive shell)"
	}
	if len(m.Command) > 60 {
		return m.Command[:57] + "..."
	}
	return m.Command
}

func newSessionsShowCmd() *cobra.Command {
	var (
		metaOnly bool
		castOut  bool
	)
	cmd := &cobra.Command{
		Use:   "show <session-id>",
		Short: "Print what happened in a recorded session",
		Long: `Prints the session's output as plain text — what you would have seen on
the terminal. Add --meta for just the metadata (who, when, exit code), or
--cast for the asciicast v2 recording itself, timing included.

For a timed replay, point asciinema at the recording:
  asciinema play "$(ownbasectl sessions path <session-id>)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if metaOnly && castOut {
				return withExitCode(exitUsage, fmt.Errorf("--meta and --cast print different things; pass one"))
			}
			m, err := sshsession.Find("", args[0])
			if err != nil {
				return err
			}
			if metaOnly {
				return printJSON(m)
			}
			if castOut {
				// The recording verbatim, so a player replays it with the
				// original timing. The desktop app reads it this way rather
				// than opening the file itself, which keeps the CLI the only
				// thing that knows where recordings live.
				return copyCastToStdout(m.CastPath)
			}
			text, err := sshsession.Transcript(m.CastPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "# %s on %s (%s), exit %s\n",
				m.ID, m.Base, m.StartedAt.Local().Format(time.RFC1123), formatSessionExit(m))
			fmt.Print(text)
			return nil
		},
	}
	cmd.Flags().BoolVar(&metaOnly, "meta", false, "print only the session metadata, as JSON")
	cmd.Flags().BoolVar(&castOut, "cast", false, "print the asciicast v2 recording itself, for a player")
	return cmd
}

func copyCastToStdout(castPath string) error {
	f, err := os.Open(castPath)
	if err != nil {
		return fmt.Errorf("open recording %s: %w", castPath, err)
	}
	defer f.Close()
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return fmt.Errorf("read recording %s: %w", castPath, err)
	}
	return nil
}

func newSessionsPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path <session-id>",
		Short: "Print the file path of a recording, for asciinema or your own tools",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := sshsession.Find("", args[0])
			if err != nil {
				return err
			}
			fmt.Println(m.CastPath)
			return nil
		},
	}
}
