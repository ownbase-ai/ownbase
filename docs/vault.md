# The vault

> Where every credential that reaches a Base lives: one encrypted file you choose the location of, and a small resident process that holds it open so the CLI, the app, and an AI agent can all use it without any of them handling your master password or your private keys.

## What it is

A **vault** is a single [KDBX 4](https://keepass.info/help/kb/kdbx.html) file — the KeePass database format. It holds, per Base:

| | |
|---|---|
| Host, SSH user, SSH port | how to reach the machine |
| Owner SSH private key | the credential that gets you in. **This is the only copy** |
| Daemon API token | the Bearer token for the Base's HTTP API |
| Config repo URL and branch | where that Base's `ownbase.yaml` lives — the operator's **trust anchor** (see below), not a cache |
| Backup repo URL + restic password (+ cloud keys) | so `restore` does not need flags after `backup setup` |

There is no other client-side state that matters. `~/.ownbase/` alongside it holds only non-secrets: a text file pointing at the vault, `known_hosts`, the agent's log, and your session recordings.

## Why KDBX, and not a file of our own

The credentials that reach a Base are the most valuable things on your computer. Two properties matter, and they pull in opposite directions unless you pick the format carefully.

**It has to be encrypted.** The obvious alternative — a YAML file in your home directory at mode 600 — is readable by every process you run. A single malicious npm postinstall or a leaked shell history is enough. Given that OwnBase's entire premise is handing an agent root on a machine, "any program you run can read the key to it" is not a defensible default.

**It has to stay yours if OwnBase disappears.** A bespoke encrypted blob would mean the keys to your infrastructure are only recoverable by our code. That is exactly the vendor lock-in [MISSION.md](../MISSION.md) exists to refuse, just relocated from a cloud service to a file format.

KDBX satisfies both. It is an open format with more than a decade of independent implementations — KeePassXC on desktop, KeePassium and Strongbox on iOS, KeePassDX on Android, and libraries in every language. If OwnBase vanished tomorrow you would open the file in KeePassXC and read your keys out by hand. That escape hatch is the point.

The deliberate trade-off: your owner SSH keys are **not** files in `~/.ssh` any more. Standard tools cannot reach them by path. The compensation is the ssh-agent socket described below, which lets `ssh`, `git`, `scp`, and `rsync` use the keys without either of you touching the file.

## Where it lives

You choose, at `vault init`. The location is recorded in `~/.ownbase/locator` (JSON, mode 600). Older installs used a plain path in `~/.ownbase/vault`; that still works.

### Local file

```bash
ownbasectl vault init ~/Library/Mobile\ Documents/com~apple~CloudDocs/OwnBase   # iCloud Drive
ownbasectl vault init ~/Dropbox/OwnBase                                         # Dropbox
ownbasectl vault init ~/ownbase.kdbx                                            # just this machine
```

Give it a directory and `ownbase.kdbx` is appended; give it a file path and that path is used. A synced folder is fine — the file is ciphertext without the master password.

### Object storage (recommended for headless recovery)

```bash
ownbasectl vault init \
  --bucket my-vault-bucket \
  --region auto \
  --endpoint https://<account>.r2.cloudflarestorage.com \
  --access-key-id … \
  --secret-access-key …
```

Works with any S3-compatible API (AWS S3, Cloudflare R2, Backblaze B2, MinIO, Wasabi). The vault is one object (default key `ownbase/vault/ownbase.kdbx`). Prefer a **dedicated vault bucket** whose credentials the Base never holds — backup credentials for the Base should be scoped to a separate backup bucket so a compromised Base cannot delete your vault.

After a remote init, save the recovery string that is printed (or re-print with `ownbasectl vault recovery-string`). On a fresh machine:

```bash
ownbasectl vault open --recovery 'ownbase-recovery-v1:…'
```

That string plus the master password is everything you need. A local ciphertext cache under `~/.ownbase/cache/` keeps reads working offline after the first successful fetch; writes always go to the live store.

`vault init` refuses to overwrite an existing vault. Pointing it at one that already exists just records the location (how a second machine joins).

`$OWNBASE_VAULT` overrides the recorded location for one invocation (local path only).

## The master password

It is the only thing protecting the file, and it is never stored anywhere. Choose it accordingly: a long passphrase you can type, not a short one you can guess.

The file is encrypted with ChaCha20 under a key derived by **Argon2** (KDBX 4 / Argon2d via gokeepasslib) — 64 MiB of memory, tuned so one derivation costs a few hundred milliseconds. New master passwords must be at least 12 characters. That cost is what makes a human-typed password worth relying on: it is imperceptible when you unlock, and it makes an offline guessing attack against a stolen copy of the file expensive per attempt rather than free. Those parameters are stored in the file header, so they survive being opened by other KDBX clients.

Every write re-randomizes the seeds and the cipher nonce, so two versions of the file — which cloud storage keeps by design — never share a keystream.

Change it with `ownbasectl vault passwd`. There is no recovery: nothing in OwnBase, no vendor, and no support channel can open the file without it. That is the same property that makes it genuinely yours.

**An agent must never be given the master password.** Ask the user to run `vault init`, `vault unlock`, and `vault passwd` themselves, or to use the app.

## The credential agent

Typing a master password before every command would be intolerable, and caching it in a file would defeat the encryption. So OwnBase does what `ssh-agent` does: a small resident process holds the decrypted vault in memory, and everything else asks it.

```bash
ownbasectl vault unlock          # prompts, starts the agent if needed
ownbasectl vault status          # is it running? unlocked? when does it lock?
ownbasectl vault lock            # forget the password now
ownbasectl agent stop            # shut the agent down entirely
```

The agent starts on demand and outlives the shell that started it, so the CLI works in a headless terminal with the desktop app closed. It logs to `~/.ownbase/agent.log`, or to the terminal if you run `ownbasectl agent run` in the foreground.

It listens on two unix sockets in `~/.ownbase`, both mode 600:

| Socket | Protocol | Purpose |
|---|---|---|
| `agent.sock` | JSON | profiles, lock state, vault writes |
| `ssh-agent.sock` | standard ssh-agent | signing with the owner keys |

Splitting them is what keeps private keys inside the agent. A command asking for a profile gets the host, the port, the token, and the *public* key — never the private half. To authenticate, it asks the second socket to sign a challenge. So `ownbasectl status mybase` opens an SSH connection with no key material anywhere in its own memory, and an AI agent driving the CLI has nothing to leak even in principle.

Those socket permissions are the whole access-control story: any process running as your user can use the agent while it is unlocked. That is the same trust boundary as `ssh-agent`, and the same as a browser holding your session cookies. What it buys is that the *file* stays encrypted at rest and the *keys* never hit disk.

The agent refuses `ssh-add`, `ssh-add -d`, and `ssh-add -x`. The vault decides which keys exist and when they are locked; a client that could add or lock one could also quietly swap the key a Base is reached with, or force a full re-unlock from a routine `ssh` helper. Lock the vault with `ownbasectl vault lock` (or the desktop app on quit).

### Auto-locking

The vault locks itself after four hours idle by default, which is long enough that a day of work does not mean retyping the password and short enough that an unattended laptop does not stay open indefinitely.

```bash
ownbasectl vault unlock --idle-timeout 30m   # tighter
ownbasectl vault unlock --idle-timeout 0     # never auto-lock
```

The desktop app can also lock on quit. A locked vault makes every Base command fail with **exit code 7**, distinct from every other failure so an unattended caller can tell "a human needs to type a password" apart from "something is broken".

## Using the keys outside OwnBase

Point `SSH_AUTH_SOCK` at the agent. Your normal `ssh`, `git`, `scp`, and `rsync` then authenticate with the owner keys, without either of you ever reading them:

```bash
export SSH_AUTH_SOCK="$(ownbasectl vault status --json | jq -r .ssh_agent_socket)"
ssh root@<base-host>
```

For a shell on a Base, prefer `ownbasectl ssh <base>`: same authentication, plus a recording ([cli.md](cli.md#ssh-and-session-audit)).

If you want a key as an ordinary file — to hand to a tool that cannot use an agent — open the vault in KeePassXC and copy the `PrivateKey` field out of the Base's entry. OwnBase deliberately has no "export my private key" command: it is the one operation where the honest answer is that you are choosing to make a copy, and doing it by hand is the right amount of friction.

## Opening the vault without OwnBase

This is the escape hatch, and it is worth testing once so you know it works.

Install [KeePassXC](https://keepassxc.org), open the file, enter the master password. You will see:

```
OwnBase/
  Bases/
    mybase          ← one entry per Base
      Title         mybase
      URL           203.0.113.10
      UserName      root
      Password      the daemon API token
      PrivateKey    the owner SSH key, OpenSSH PEM
      PublicKey     the authorized_keys line
      SSHPort, APIPort, ConfigRepoURL, ConfigRef, LocalVM
```

Standard KeePass fields are used where they fit — `Title`, `URL`, `UserName`, `Password` — so the entry is legible in any client rather than being a bag of custom attributes.

You can keep your own passwords in the same file. OwnBase only ever replaces the `OwnBase` group when it writes, and it re-reads the file first if something else changed it, so your entries survive. It is still a shared mutable file: avoid saving from KeePassXC and `ownbasectl` at the same instant, the same way you would with any document in a synced folder.

## Config repo pin is a trust anchor

`ConfigRepoURL` / `ConfigRef` in the vault are not a mirror of whatever the Base last reported. They are the operator's pin of which config repo this Base should track:

- `status` / `checkup` may **backfill** an empty pin from the Base (older profiles).
- They **never overwrite** a non-empty pin when the Base reports a different URL — they warn instead. A rooted Base can rewrite `/opt/ownbase/config-source.yaml`; the vault must not follow it blindly.
- **Intentional repoint:** `ownbasectl config setup <base> --repo <url>`.
- **Rebuild:** `ownbasectl restore` re-asserts the vault pin onto the Base after the daemon is healthy and before success is reported. Losing the vault means losing that anchor for the next rebuild.

See [identity-and-authority.md](foundation/identity-and-authority.md) and [troubleshooting.md](troubleshooting.md).

## Backing it up

The vault is small and changes rarely. Two things worth knowing:

- **A synced folder is already a backup**, and its version history is what recovers you from a corrupt write. This is most of why it is the recommended location.
- **The vault is not in your Base backups, on purpose.** Your restic repository is reachable *using* credentials from the vault, so a vault backed up only there would be unrecoverable exactly when you needed it. Keep it somewhere that does not depend on a Base being alive.
- **The restic password is a root recovery secret.** Snapshots include `/opt/ownbase/secrets` and the age key, so repo URL + restic password is a complete export of every secret the Base has held. The vault escrows a copy for convenience (`backup recovery-kit` reprints it); keep an offline copy from the recovery kit printed at `backup setup` / `backup rekey`. The vault is not a recovery service.

Losing the vault means losing SSH access to every Base in it. The machines keep running and their data is intact — a Base does not depend on your vault — but you would need to reach them another way: the provider's console, or a rebuild from backups with `ownbasectl restore` (if you still hold the restic password offline).

## Files at a glance

| Path | Contents | Secret? |
|---|---|---|
| the vault file, wherever you put it | every credential, encrypted | yes, and encrypted at rest |
| `~/.ownbase/vault` | a text file holding the vault's path | no |
| `~/.ownbase/agent.sock` | control socket, mode 600 | a live handle to an unlocked vault |
| `~/.ownbase/ssh-agent.sock` | ssh-agent socket, mode 600 | a live handle to the owner keys |
| `~/.ownbase/agent.log` | the agent's own diagnostics | no |
| `~/.ownbase/known_hosts` | Base host keys, trust-on-first-use | no |
| `~/.ownbase/sessions/` | session recordings, mode 600 | can contain anything typed at a prompt |

## See also

- [cli.md](cli.md#vault-and-credential-agent) — every vault and agent command, with flags.
- [desktop.md](desktop.md) — the app's unlock screen and vault view.
- [decisions.md](decisions.md) — why KDBX, why a resident agent, and what was rejected.
- [troubleshooting.md](troubleshooting.md#ownbasectl-will-not-run) — "the vault is locked", "no vault configured", "wrong master password".
