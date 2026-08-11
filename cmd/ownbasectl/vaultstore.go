package main

// vaultstore.go is how every command in ownbasectl reaches a Base's
// credentials. There is exactly one path: ask the credential agent
// (internal/agentd), which holds the unlocked KDBX vault in memory.
//
// Nothing here reads a private key. A profile arrives with its host, ports,
// API token, and public key; authentication happens by asking the agent's
// ssh-agent socket to sign a challenge. That is what lets a coding agent drive
// ownbasectl without ever having key material in its own process, and it is
// why sshTarget — not a key path — is the only way a command names a Base to
// the SSH transport.

import (
	"errors"
	"fmt"
	"os"

	"github.com/ownbase/ownbase/internal/agentd"
	"github.com/ownbase/ownbase/internal/tunnel"
	"github.com/ownbase/ownbase/internal/vault"
)

// lockedHint is the guidance attached to every "the vault is locked" failure.
// A caller — human or agent — needs to know the single next command, not that
// a socket returned an error.
const lockedHint = `the vault is locked.

  Unlock it with:
    ownbasectl vault unlock

  Or open the OwnBase app, which unlocks it for you. Set one up first with:
    ownbasectl vault init`

// agentClient returns a client for the credential agent, starting the agent if
// it is not already running.
func agentClient() (*agentd.Client, error) {
	if err := agentd.EnsureRunning(""); err != nil {
		return nil, err
	}
	return agentd.NewClient()
}

// vaultError translates agent failures into the guidance and exit codes the
// CLI contract promises.
func vaultError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentd.ErrLocked):
		return withExitCode(exitLocked, errors.New(lockedHint))
	case errors.Is(err, vault.ErrNoVault):
		return withExitCode(exitLocked, errors.New(
			"no vault configured yet — create one with:\n    ownbasectl vault init"))
	default:
		return err
	}
}

// loadProfile reads the named Base's profile from the vault. The private key is
// never part of it.
func loadProfile(base string) (vault.Profile, error) {
	c, err := agentClient()
	if err != nil {
		return vault.Profile{}, err
	}
	p, err := c.Get(base)
	if err != nil {
		return vault.Profile{}, vaultError(err)
	}
	return p, nil
}

// isMissingBase reports whether err is "no such Base in the vault", which the
// commands that create a Base treat as the normal starting state rather than a
// failure.
func isMissingBase(err error) bool { return errors.Is(err, vault.ErrNotFound) }

// putProfile stores a Base's profile. Empty secret fields leave whatever the
// vault already holds for this Base in place (see Profile.MergeSecretsFrom).
func putProfile(base string, p vault.Profile) error {
	c, err := agentClient()
	if err != nil {
		return err
	}
	return vaultError(c.Put(base, p))
}

// loadBackupCreds returns the restic restore material stored for base.
func loadBackupCreds(base string) (vault.BackupCredentials, error) {
	c, err := agentClient()
	if err != nil {
		return vault.BackupCredentials{}, err
	}
	creds, err := c.GetBackup(base)
	if err != nil {
		return vault.BackupCredentials{}, vaultError(err)
	}
	return creds, nil
}

// saveProfile applies mutate to an existing Base's profile and persists it.
// A Base that does not exist yet is refused rather than half-created: a typo'd
// name must not produce a profile with a config repo but no host or token.
func saveProfile(base string, mutate func(*vault.Profile)) error {
	p, err := loadProfile(base)
	if err != nil {
		return err
	}
	mutate(&p)
	return putProfile(base, p)
}

// deleteProfile removes a Base's profile and its owner key from the vault.
func deleteProfile(base string) error {
	c, err := agentClient()
	if err != nil {
		return err
	}
	return vaultError(c.Delete(base))
}

// listBases returns the configured Base names, sorted.
func listBases() ([]string, error) {
	c, err := agentClient()
	if err != nil {
		return nil, err
	}
	names, err := c.List()
	if err != nil {
		return nil, vaultError(err)
	}
	return names, nil
}

// sshTarget builds the SSH transport target for a profile: where to dial, and
// which of the agent's keys to sign with.
func sshTarget(p vault.Profile) (tunnel.Target, error) {
	t := tunnel.Target{
		Host:      p.Host,
		User:      p.EffectiveSSHUser(),
		Port:      p.EffectiveSSHPort(),
		PublicKey: p.PublicKey,
	}
	sock, err := agentd.SSHAgentSocketPath()
	if err != nil {
		return t, err
	}
	t.AgentSocket = sock
	return t, nil
}

// baseTarget resolves a Base name straight to an SSH target.
func baseTarget(base string) (tunnel.Target, vault.Profile, error) {
	p, err := loadProfile(base)
	if err != nil {
		return tunnel.Target{}, vault.Profile{}, err
	}
	if p.Host == "" {
		return tunnel.Target{}, p, fmt.Errorf(
			"Base %q has no host recorded — it has an owner key from 'keygen' but was never created; run: ownbasectl create %s --remote root@<ip>", base, base)
	}
	t, err := sshTarget(p)
	return t, p, err
}

// readPrivateKeyFile loads a private key the user already has — one a provider
// authorized before OwnBase existed — so it can be moved into the vault. The
// file on disk is left alone; the vault becomes the copy ownbasectl uses.
func readPrivateKeyFile(path string) (privatePEM, publicLine string, err error) {
	data, err := os.ReadFile(vault.ExpandTilde(path))
	if err != nil {
		return "", "", fmt.Errorf("read SSH key %s: %w", path, err)
	}
	p := vault.Profile{PrivateKey: string(data)}
	signer, err := p.Signer()
	if err != nil {
		return "", "", fmt.Errorf("%s: %w\n  A passphrase-protected key cannot be imported unattended — decrypt it first with 'ssh-keygen -p'", path, err)
	}
	return string(data), vault.AuthorizedKeyLine(signer.PublicKey(), ""), nil
}
