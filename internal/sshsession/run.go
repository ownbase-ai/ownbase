package sshsession

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/ownbase/ownbase/internal/tunnel"
)

// Options describe one session to open on a Base.
type Options struct {
	// Base is the Base name, used to file the recording.
	Base string
	// Target is where and how to connect.
	Target tunnel.Target
	// Command is a single command to run instead of an interactive shell.
	Command string
	// Stdin, Stdout, Stderr default to the process's own.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// ForceTTY requests a PTY even for a one-shot command; ForceNoTTY
	// refuses one even for an interactive session (used when stdin is a pipe).
	ForceTTY   bool
	ForceNoTTY bool
	// Invoker labels who opened the session in the audit trail.
	Invoker string
}

// Result is the outcome of a session.
type Result struct {
	Meta     Meta
	ExitCode int
}

// Run opens a session on the Base, streams it to the caller's terminal, and
// records the whole thing. It returns after the remote side exits.
//
// The recording is not optional and cannot be turned off. That is the point of
// routing shells through here: a Base is a machine an agent has root on, and an
// audit trail with an opt-out is not an audit trail.
func Run(opts Options) (*Result, error) {
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	wantTTY := opts.Command == "" || opts.ForceTTY
	if opts.ForceNoTTY {
		wantTTY = false
	}
	// A PTY is only useful if there is a terminal on this end to drive it.
	stdinFD, stdinIsTerminal := terminalFD(stdin)
	if wantTTY && !stdinIsTerminal {
		wantTTY = false
	}

	width, height := 120, 40
	if outFD, ok := terminalFD(stdout); ok {
		if w, h, err := term.GetSize(outFD); err == nil && w > 0 && h > 0 {
			width, height = w, h
		}
	}

	rec, err := NewRecorder(Meta{
		ID:          NewID(),
		Base:        opts.Base,
		Host:        opts.Target.Host,
		User:        opts.Target.User,
		Command:     opts.Command,
		Interactive: wantTTY,
		Invoker:     opts.Invoker,
	}, width, height)
	if err != nil {
		return nil, err
	}

	// failEarly records the abandoned attempt (so it is on disk and in
	// `sessions list` like any other) and still returns a Result carrying its
	// Meta, so callers such as `ownbasectl ssh` can print --json metadata and
	// the "session recorded as ..." notice for a failure exactly as they
	// would for one that ran to completion.
	failEarly := func(wrapped error) (*Result, error) {
		_ = rec.Finish(-1, wrapped)
		return &Result{Meta: rec.Meta(), ExitCode: -1}, wrapped
	}

	client, err := tunnel.Dial(opts.Target)
	if err != nil {
		return failEarly(err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return failEarly(fmt.Errorf("open ssh session: %w", err))
	}
	defer sess.Close()

	sess.Stdout = rec.Tee(stdout)
	sess.Stderr = rec.Tee(stderr)

	remoteStdin, err := sess.StdinPipe()
	if err != nil {
		return failEarly(fmt.Errorf("attach stdin: %w", err))
	}

	var restore func()
	if wantTTY {
		state, rerr := term.MakeRaw(stdinFD)
		if rerr != nil {
			return failEarly(fmt.Errorf("put the terminal in raw mode: %w", rerr))
		}
		restore = func() { _ = term.Restore(stdinFD, state) }
		defer restore()

		termType := os.Getenv("TERM")
		if termType == "" {
			termType = "xterm-256color"
		}
		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
		if perr := sess.RequestPty(termType, height, width, modes); perr != nil {
			return failEarly(fmt.Errorf("request a terminal on the Base: %w", perr))
		}

		if outFD, ok := terminalFD(stdout); ok {
			stopResize := watchResize(sess, outFD, width, height)
			defer stopResize()
		}
	}

	// Copy our stdin to the remote, recording it. The goroutine is deliberately
	// not waited on: a blocking read on an interactive terminal never returns
	// once the remote side has exited, and the session's exit status is the
	// thing that decides when we are done.
	go func() {
		_, _ = io.Copy(rec.TeeInput(remoteStdin), stdin)
		_ = remoteStdin.Close()
	}()

	var runErr error
	if opts.Command == "" {
		if runErr = sess.Shell(); runErr == nil {
			runErr = sess.Wait()
		}
	} else {
		runErr = sess.Run(opts.Command)
	}

	exitCode, sessionErr := classify(runErr)
	if restore != nil {
		restore()
	}
	if ferr := rec.Finish(exitCode, sessionErr); ferr != nil {
		return nil, ferr
	}
	return &Result{Meta: rec.Meta(), ExitCode: exitCode}, sessionErr
}

// watchResize forwards local terminal resizes to the remote PTY, so a full
// screen program on the Base does not keep drawing at the size the window had
// when the session opened. Returns a function that stops watching.
func watchResize(sess *ssh.Session, fd, width, height int) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				w, h, err := term.GetSize(fd)
				if err != nil || w <= 0 || h <= 0 || (w == width && h == height) {
					continue
				}
				width, height = w, h
				_ = sess.WindowChange(h, w)
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// classify separates "the remote command exited non-zero" — which is
// information, not a failure of ours — from "the session broke".
func classify(err error) (exitCode int, sessionErr error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	// A clean interactive logout arrives as a missing exit status rather than
	// a zero one, and is not worth reporting as an error.
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return 0, nil
	}
	if strings.Contains(err.Error(), "exited without exit status") {
		return 0, nil
	}
	return -1, err
}

// terminalFD returns the file descriptor behind r/w when it is a terminal.
func terminalFD(v any) (int, bool) {
	f, ok := v.(*os.File)
	if !ok {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
}
