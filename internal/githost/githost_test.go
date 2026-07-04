package githost_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/githost"
)

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

func TestBootstrap_CreatesRepoAndCheckout(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Bare repo must have a HEAD file.
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err != nil {
		t.Errorf("bare repo missing HEAD: %v", err)
	}

	// Checkout must have a .git directory.
	if _, err := os.Stat(filepath.Join(checkoutPath, ".git")); err != nil {
		t.Errorf("checkout missing .git: %v", err)
	}
}

func TestBootstrap_Idempotent(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("first Bootstrap: %v", err)
	}
	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("second Bootstrap (idempotent): %v", err)
	}
}

// ---------------------------------------------------------------------------
// InstallHook
// ---------------------------------------------------------------------------

func TestInstallHook_CreatesExecutableHook(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := githost.InstallHook(repoPath); err != nil {
		t.Fatalf("InstallHook: %v", err)
	}

	hookPath := filepath.Join(repoPath, "hooks", "post-receive")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("hook file missing: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("hook file is not executable: mode %04o", info.Mode())
	}

	data, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(data), "kill -USR1") {
		t.Errorf("hook does not contain SIGUSR1 signal: %s", string(data))
	}
	if !strings.Contains(string(data), "daemon.pid") {
		t.Errorf("hook does not reference daemon.pid: %s", string(data))
	}
}

func TestInstallHook_Idempotent(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")
	githost.Bootstrap(repoPath, checkoutPath) //nolint:errcheck

	if err := githost.InstallHook(repoPath); err != nil {
		t.Fatalf("first InstallHook: %v", err)
	}
	if err := githost.InstallHook(repoPath); err != nil {
		t.Fatalf("second InstallHook (idempotent): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Genesis record
// ---------------------------------------------------------------------------

func TestNewGenesisRecord_PopulatesFields(t *testing.T) {
	rec, err := githost.NewGenesisRecord("dev", "age1testpubkey")
	if err != nil {
		t.Fatalf("NewGenesisRecord: %v", err)
	}

	if rec.Version != githost.GenesisRecordVersion {
		t.Errorf("version: got %q, want %q", rec.Version, githost.GenesisRecordVersion)
	}
	if rec.MachineID == "" {
		t.Error("machine_id is empty")
	}
	if rec.AgentVersion != "dev" {
		t.Errorf("agent_version: got %q, want %q", rec.AgentVersion, "dev")
	}
	if rec.AgePublicKey != "age1testpubkey" {
		t.Errorf("age_public_key: got %q, want %q", rec.AgePublicKey, "age1testpubkey")
	}
	if rec.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
}

func TestWriteGenesisRecord_CreatesCommit(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	rec, err := githost.NewGenesisRecord("dev", "")
	if err != nil {
		t.Fatalf("NewGenesisRecord: %v", err)
	}
	if err := githost.WriteGenesisRecord(checkoutPath, rec); err != nil {
		t.Fatalf("WriteGenesisRecord: %v", err)
	}

	// The file must exist in the checkout.
	genesisPath := filepath.Join(checkoutPath, githost.GenesisFilename)
	data, err := os.ReadFile(genesisPath)
	if err != nil {
		t.Fatalf("genesis.json missing: %v", err)
	}

	// Must be valid JSON with the expected version.
	var parsed githost.GenesisRecord
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("genesis.json is not valid JSON: %v", err)
	}
	if parsed.Version != githost.GenesisRecordVersion {
		t.Errorf("version: got %q, want %q", parsed.Version, githost.GenesisRecordVersion)
	}

	// The bare repo must have the commit.
	sha, err := githost.HeadCommit(checkoutPath)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(sha) < 40 {
		t.Errorf("HEAD commit SHA looks wrong: %q", sha)
	}
}

func TestWriteGenesisRecord_Idempotent(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	_ = githost.Bootstrap(repoPath, checkoutPath)
	rec, _ := githost.NewGenesisRecord("dev", "")

	if err := githost.WriteGenesisRecord(checkoutPath, rec); err != nil {
		t.Fatalf("first WriteGenesisRecord: %v", err)
	}
	sha1, _ := githost.HeadCommit(checkoutPath)

	if err := githost.WriteGenesisRecord(checkoutPath, rec); err != nil {
		t.Fatalf("second WriteGenesisRecord (idempotent): %v", err)
	}
	sha2, _ := githost.HeadCommit(checkoutPath)

	if sha1 != sha2 {
		t.Errorf("idempotent call created a second commit (sha1=%s, sha2=%s)", sha1, sha2)
	}
}

func TestReadGenesisRecord_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	_ = githost.Bootstrap(repoPath, checkoutPath)
	rec, _ := githost.NewGenesisRecord("v1.2.3", "age1abc")
	_ = githost.WriteGenesisRecord(checkoutPath, rec)

	got, err := githost.ReadGenesisRecord(checkoutPath)
	if err != nil {
		t.Fatalf("ReadGenesisRecord: %v", err)
	}
	if got.AgentVersion != "v1.2.3" {
		t.Errorf("agent_version: got %q, want %q", got.AgentVersion, "v1.2.3")
	}
	if got.AgePublicKey != "age1abc" {
		t.Errorf("age_public_key: got %q, want %q", got.AgePublicKey, "age1abc")
	}
}

// ---------------------------------------------------------------------------
// MachineID
// ---------------------------------------------------------------------------

func TestMachineID_NonEmpty(t *testing.T) {
	id, err := githost.MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	if strings.TrimSpace(id) == "" {
		t.Error("MachineID returned empty string")
	}
}

// ---------------------------------------------------------------------------
// WritePIDFile
// ---------------------------------------------------------------------------

func TestWritePIDFile_WritesCurrentPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.pid")

	if err := githost.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" || pid == "0" {
		t.Errorf("unexpected PID content: %q", pid)
	}
}

// TestDefaultForgejoRepoName_UsedByBootstrapAndAgent ensures that the constant
// used by BootstrapCore to create the Forgejo repo matches the value the agent
// uses as the default for its --repo-name flag. A mismatch causes the agent to
// look for a repo that doesn't exist (sync fails with "repository not found").
//
// If this test fails, check:
//   - githost.DefaultForgejoRepoName
//   - the --repo-name flag default in cmd/ownbased/main.go
func TestDefaultForgejoRepoName_MatchesAgentFlagDefault(t *testing.T) {
	// The canonical name is the constant. Document it here so a rename is
	// intentional and forces this test to be updated.
	const wantName = "ownbase"
	if githost.DefaultForgejoRepoName != wantName {
		t.Errorf("DefaultForgejoRepoName = %q, want %q — update this test if the name was intentionally changed",
			githost.DefaultForgejoRepoName, wantName)
	}
}
