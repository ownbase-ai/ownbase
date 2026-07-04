package githost

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GenesisRecordVersion is the schema version for the genesis record.
const GenesisRecordVersion = "v1"

// GenesisFilename is the name of the genesis record file committed to the repo.
const GenesisFilename = "genesis.json"

// GenesisRecord is the signed provenance record written into the bare repo on
// the very first reconcile. It captures who bootstrapped the Base, on which
// machine, with which agent version, and which age public key was used so
// encrypted secrets can be verified.
//
// The record is committed into the repo — its integrity is guaranteed by the
// git commit hash, which is in turn stored in the audit log (M3). Recovery
// (M6) and updates (M7) diff against it to understand what was originally
// installed.
type GenesisRecord struct {
	Version      string    `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	MachineID    string    `json:"machine_id"`
	Hostname     string    `json:"hostname"`
	AgentVersion string    `json:"agent_version"`
	// AgePublicKey is the age X25519 recipient public key for this Base.
	// Secrets encrypted with this key can be decrypted on (and only on) this
	// Base. Included here so the reconstruction procedure can verify that the
	// secrets file and the Base key match before attempting a restore (M6).
	AgePublicKey string `json:"age_public_key,omitempty"`
}

// NewGenesisRecord constructs a GenesisRecord for the current machine.
// agentVersion and agePubKey may be empty strings if not yet available.
func NewGenesisRecord(agentVersion, agePubKey string) (GenesisRecord, error) {
	machineID, err := MachineID()
	if err != nil {
		return GenesisRecord{}, fmt.Errorf("genesis record: machine id: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return GenesisRecord{
		Version:      GenesisRecordVersion,
		CreatedAt:    time.Now().UTC(),
		MachineID:    machineID,
		Hostname:     host,
		AgentVersion: agentVersion,
		AgePublicKey: agePubKey,
	}, nil
}

// WriteGenesisRecord writes the genesis record to the checkout, commits it,
// and pushes the commit to the bare repo. The commit message includes the
// machine ID and agent version for human readability.
//
// WriteGenesisRecord is idempotent: if genesis.json already exists in the
// checkout it does nothing and returns nil.
func WriteGenesisRecord(checkoutPath string, record GenesisRecord) error {
	genesisPath := filepath.Join(checkoutPath, GenesisFilename)

	// Idempotent: already written.
	if _, err := os.Stat(genesisPath); err == nil {
		return nil
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("genesis: marshal: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		return fmt.Errorf("genesis: write %s: %w", genesisPath, err)
	}

	if err := gitCommit(checkoutPath, GenesisFilename,
		fmt.Sprintf("genesis: bootstrap Base on %s (agent %s)",
			record.Hostname, record.AgentVersion),
	); err != nil {
		return fmt.Errorf("genesis: commit: %w", err)
	}

	if err := gitPush(checkoutPath); err != nil {
		return fmt.Errorf("genesis: push: %w", err)
	}

	return nil
}

// ReadGenesisRecord reads and parses the genesis.json from the checkout.
// Returns an error if the file does not exist or cannot be parsed.
func ReadGenesisRecord(checkoutPath string) (GenesisRecord, error) {
	data, err := os.ReadFile(filepath.Join(checkoutPath, GenesisFilename))
	if err != nil {
		return GenesisRecord{}, fmt.Errorf("genesis: read: %w", err)
	}
	var rec GenesisRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return GenesisRecord{}, fmt.Errorf("genesis: unmarshal: %w", err)
	}
	return rec, nil
}

// ---------------------------------------------------------------------------
// git helpers
// ---------------------------------------------------------------------------

func gitCommit(repoPath, filename, message string) error {
	addOut, err := exec.Command("git", "-C", repoPath, "add", filename).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git add %s: %w\n%s", filename, err, addOut)
	}
	commitOut, err := exec.Command(
		"git", "-C", repoPath,
		"commit", "-m", message,
		"--author", "OwnBase Daemon <daemon@ownbase.local>",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, commitOut)
	}
	return nil
}

func gitPush(repoPath string) error {
	out, err := exec.Command("git", "-C", repoPath, "push", "origin", "main").CombinedOutput()
	if err != nil {
		// If upstream doesn't have the branch yet, push with -u.
		out2, err2 := exec.Command("git", "-C", repoPath, "push", "-u", "origin", "main").CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("git push: %w\n%s\n%s", err, out, out2)
		}
	}
	_ = out
	return nil
}

// HeadCommit returns the current HEAD commit SHA of the checkout.
func HeadCommit(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
