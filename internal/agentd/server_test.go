package agentd_test

// Tier-1 tests for agent Serve startup. The property under test is the one
// Bugbot flagged: concurrent Serves must not race on probe→unlink→bind, or
// the loser unlinks the winner's live sockets and clients land on an empty
// agent while the original still holds the unlocked vault.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/agentd"
)

// shortHome sets HOME to a short path under /tmp. macOS caps unix socket
// paths at ~104 bytes and t.TempDir() is already too long for
// $HOME/.ownbase/agent.sock.
func shortHome(t *testing.T) {
	t.Helper()
	home := filepath.Join(os.TempDir(), fmt.Sprintf("ob-ag-%d", time.Now().UnixNano()%1e12))
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
}

func TestServe_SecondInstanceRefusesWhileFirstIsUp(t *testing.T) {
	shortHome(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- agentd.NewServer("test").Serve(ctx)
	}()

	// Wait until the first agent answers before starting the second.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if agentUp(t) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first agent did not come up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	err := agentd.NewServer("test-2").Serve(context.Background())
	if err == nil {
		t.Fatal("second Serve: expected already-running error, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("second Serve error = %v, want already running", err)
	}

	// First agent must still answer after the second failed.
	if !agentUp(t) {
		t.Error("first agent stopped answering after a competing Serve")
	}

	cancel()
	select {
	case <-firstErr:
	case <-time.After(3 * time.Second):
		t.Error("first Serve did not return after cancel")
	}
}

// Two Serves started at once must end with exactly one winner. Without the
// flock, both can pass probe, the loser unlinks the winner's sockets, and
// probe then sees a brand-new empty agent (or nothing).
func TestServe_ConcurrentStartOnlyOneWins(t *testing.T) {
	shortHome(t)

	const n = 8
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started int
		refused int
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			err := agentd.NewServer("test").Serve(ctx)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case strings.Contains(err.Error(), "already running"):
				refused++
			default:
				t.Errorf("Serve: unexpected error: %v", err)
			}
		}()
	}

	// Give contenders time to serialize on the lock and bind.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if agentUp(t) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			t.Fatal("no agent came up from concurrent Serves")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The live agent must stay up while losers exit.
	time.Sleep(100 * time.Millisecond)
	if !agentUp(t) {
		cancel()
		wg.Wait()
		t.Fatal("agent socket died during concurrent start — loser likely unlinked the winner")
	}

	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if started != 1 {
		t.Errorf("winners = %d, want 1 (refused=%d)", started, refused)
	}
	if refused != n-1 {
		t.Errorf("refused = %d, want %d", refused, n-1)
	}
}

func agentUp(t *testing.T) bool {
	t.Helper()
	c, err := agentd.NewClient()
	if err != nil {
		return false
	}
	st, err := c.Status()
	if err != nil {
		return false
	}
	return st.Running
}
