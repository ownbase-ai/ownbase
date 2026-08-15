# Troubleshooting

> What to do when something fails. Each section starts from the symptom you'd actually see.

The single most useful diagnostic for anything happening *on* the Base is the daemon journal:

```bash
ownbasectl ssh <name> -- journalctl -u ownbased -n 200   # any Base, recorded
ownbasectl ssh <name>                                    # then: journalctl -u ownbased -f
```

Use `ownbasectl ssh` rather than plain `ssh`: it finds the key in your vault and records the session, so there is a trail of the debugging itself.

If `ownbasectl` will not run at all, the problem is on your machine — jump to [ownbasectl will not run](#ownbasectl-will-not-run).

---

## `ownbasectl` will not run

### "the vault is locked" (exit code 7)

Every credential lives in your vault, and the credential agent has forgotten the master password — either it auto-locked after four idle hours, the machine rebooted, or something ran `vault lock`.

```bash
ownbasectl vault unlock
```

`ownbasectl vault status` shows whether the agent is running, whether the vault is open, and when it will lock next. Raise or disable the timeout with `ownbasectl vault unlock --idle-timeout 8h` or `--idle-timeout 0`.

This is exit code 7 and not a generic failure precisely so an unattended caller can tell "a human needs to type a password" apart from "something is broken".

### "no vault configured"

There is no vault yet, or the pointer to it is gone. If this is a fresh machine, create one — [vault.md](vault.md):

```bash
ownbasectl vault init ~/Dropbox/OwnBase
```

If you already have a vault file (from another machine, or restored from backup), point at it instead of making a new one, or you will have an empty vault and no way to reach your Bases:

```bash
ownbasectl vault init ~/Dropbox/OwnBase/ownbase.kdbx   # records an existing file
```

### "wrong master password (or the vault file is corrupt)"

KDBX authenticates the whole file, so a bad password and a damaged file fail identically and OwnBase cannot tell you which it was. Try the password in KeePassXC: if it opens there, the problem is what you typed; if it does not, restore the file from your cloud storage's version history and try again.

---

## `create` preflight failures (exit code 3)

Preflight runs before `create` changes anything on the target, so every failure here leaves the machine exactly as it was. Fix the cause and re-run the same command.

### "did not accept SSH within 5m"

The server never came up, or is not reachable at that address. Confirm it is running in the provider's console and that the IP is right. A machine still booting just needs longer: `--wait-for-ssh 10m`. A provider firewall or security group blocking port 22 produces the same symptom.

### "rejected the key ... make sure this key was pasted into the provider's SSH key field"

The server is up but does not recognise your key, and waiting will not fix it. The usual causes:

- The key was not pasted in when the machine was created. Most providers cannot add a key to an existing machine without a rebuild — check yours before recreating it.
- A *different* key was pasted. `ownbasectl keygen <name>` prints the right one; it is idempotent, so run it again to see it.
- The login user is wrong. Provider images differ: `root` on most, `ubuntu` on some AWS and Azure images. Pass `--ssh-user ubuntu`.

### "cannot run sudo without a password prompt"

The login user needs passwordless sudo, because the installer configures the host. Either log in as root (`--ssh-user root`) or add the user to a NOPASSWD sudoers rule.

### "runs Ubuntu X, but OwnBase requires 22.04 or newer" / "is armv7l"

The image is wrong for OwnBase. Rebuild the server with a stock Ubuntu 22.04 or 24.04 image on x86_64 or aarch64. OwnBase does not support Debian, Alpine, or RHEL derivatives.

### "has N MB of RAM, below the floor" / "has an N GB root disk"

The machine is too small to build a service from source. See [sizing](../README.md#how-big-a-machine) — the floor is set by build peaks, not by what the services use at rest.

### "already points at <host>" (exit code 6)

That Base name is registered against a different machine, and overwriting it would discard the old server's API token, leaving it running and unreachable. Use a different name, remove the stale profile with `ownbasectl delete <name> --keep-vm`, or pass `--replace` if you really are moving the name.

You can also hit this when it *is* the same machine, reached by a different address — you created the Base by hostname and retried with the IP, or the other way round. The comparison is on the address as written (normalized for case, whitespace, a trailing dot, and IPv6 spelling), and it does not resolve DNS on purpose: a hostname that has since been repointed would resolve to the new server and silently approve discarding the old one's token. When you know both addresses are the same machine, `--replace` is the right answer and costs nothing.

---

## `create` / install failures

### "was installed but its daemon did not report healthy" (exit code 5)

The install succeeded and the profile is registered; the daemon just did not finish pass zero within `--wait-timeout`. It is almost certainly still working — hardening a slow or small machine can take several minutes. Watch it:

```bash
ownbasectl ssh <name> -- journalctl -u ownbased -n 200
```

Then `ownbasectl status <name>` once it settles. Nothing needs re-running.

### The installer fails partway through (pass zero)

Host hardening (Podman, UFW, fail2ban, unattended-upgrades, trivy) and the Caddy bootstrap are the daemon's **reconcile pass zero** — they run after the install script finishes, and they are **resumable**. If the daemon hits a transient failure (apt mirror down, network blip), it retries on its normal loop; watch `journalctl -u ownbased -f` and give it a few minutes before intervening.

If `create` itself failed (the script, not the daemon):

- **Remote server:** fix the cause and re-run the same `ownbasectl create ... --remote ...` — the installer is idempotent and just continues.
- **Local VM:** re-run `ownbasectl create <name>`. It will ask before deleting the half-provisioned VM and start clean.

### "Failed to download daemon binary" / "Signature verification FAILED"

The installer downloads `ownbased` from `releases.ownbase.ai/daemon` and verifies its minisign signature. A download failure usually means the Base has no outbound HTTPS (check its network/firewall at the provider). A signature failure means the binary does not match the OwnBase release key — **do not skip verification**; re-run, and if it persists, report it.

### "this is a dev build of ownbasectl..."

You are running `ownbasectl` built from source (`go build` / `go run`), which installs the daemon by building it from the OwnBase checkout. Run it from inside the repo, or install a released `ownbasectl` (`brew install --cask ownbase-ai/tap/ownbasectl`).

---

## Tunnel and SSH errors

### "host key mismatch for <host>"

`ownbasectl` verifies host keys against `~/.ownbase/known_hosts` (trust-on-first-use). A mismatch means the machine at that address presents a key that is not on file *and* the keys that *are* on file are no longer offered by the server — either you re-provisioned it, or something is intercepting the connection.

If you only ever connected once before an OpenSSH package upgrade, older clients may have recorded a single key type (often `ssh-rsa`). Current `ownbasectl` records every type and, when the old keys are still on the server, quietly fills in the missing ones on the next connect. Upgrade the CLI and retry before wiping entries.

If you re-provisioned the server (new machine at the same IP):

```bash
# remove the stale line for the host, then reconnect (the new keys are re-added on first use)
grep -v '<host>' ~/.ownbase/known_hosts > /tmp/kh && mv /tmp/kh ~/.ownbase/known_hosts
```

### "ssh: unable to authenticate" / connection refused

- Check the profile: `ownbasectl list` shows the host and login user. Remote installs connect as `root` by default; local VMs use `ubuntu`. Fix a wrong one with `ownbasectl adopt <name> --host <host> --ssh-user <user>`.
- Check the key is actually there: `ownbasectl vault status` reports how many keys are loaded, and `ownbasectl keygen <name>` prints the public half of this Base's key. If the server authorizes a different key, the fix is to import the right one: `ownbasectl keygen <name> --import <path>`.
- Confirm the key works outside ownbasectl by borrowing the agent rather than exporting the key:

```bash
SSH_AUTH_SOCK="$(ownbasectl vault status --json | jq -r .ssh_agent_socket)" ssh root@<host>
```

- If sshd listens on a non-standard port, re-register with `ownbasectl adopt <name> --host <host> --ssh-port <port>`, or pass `--ssh-port` at create time so UFW opens it and fail2ban jails the right port.

### "unauthorized — check that your token is correct"

The API token in your profile no longer matches the Base (e.g. someone ran `POST /token/reset`). Re-registering fetches the current one over SSH automatically:

```bash
ownbasectl adopt <name> --host <host>
```

If SSH can't read `/opt/ownbase/api-token` itself (a login user without sudo, say), fetch it by hand and pass it explicitly: `ownbasectl ssh <name> -- sudo cat /opt/ownbase/api-token`, then `ownbasectl adopt <name> --host <host> --token <token>`.

Services using the **unix socket** path are unaffected by token rotation — they never use the Bearer token.

---

## Daemon API from a service (`ownbase_access`)

### `403 forbidden` on the socket

The service is authenticated (socket path) but not permitted. Check:

1. `ownbase_access:` includes the scope for that route ([api.md scope table](api.md#service-access-over-unix-sockets-ownbase_access)).
2. The route is not **owner-only** (`/config/source`, `/token/reset`, `/self-update`, `/security/*`, `/db/restore`, `POST /ssh-key`, `GET /secrets/` list-all) — those refuse even `*`.
3. Config has been reconciled since you edited scopes (grants load from live config).

### Socket missing / `connect: no such file or directory`

Inside the container the socket is `/run/ownbase/api.sock`. Missing means:

- No `ownbase_access` on that service, or it was cleared.
- Reconcile has not run since the grant was added (socket dirs are created before container apply on the next reconcile).
- An old daemon that bind-mounted the socket *file* instead of the directory left a stale mount after restart — upgrade and restart the unit.

### Config proposals (`POST /config`)

| Symptom | Likely cause |
|---|---|
| `400` invalid ownbase.yaml | Schema validation failed — fix the document |
| `400` no change | Content matches the tracked tip |
| `400` branch must start with `ownbase/agent/` | Custom branch name rejected |
| `500` git push failed | Deploy key lacks **write** on the config repo, or branch protection rejected the push |
| `501` config write not available | Daemon built without `WriteConfig` wired |

After a successful push, merge on the forge, then `POST /reconcile`. The Base does not auto-apply proposal branches.

### Vault vs Base config repo mismatch

`status`/`checkup` may print: Base reports config repo X but the vault pins Y. **Do not treat this as automatic sync.** The vault pin wins on purpose (a rooted Base can rewrite `config-source.yaml`).

- Intentional repoint: `ownbasectl config setup <base> --repo <url>`
- Unintentional: investigate the Base before changing the vault

### Restore failed re-asserting config source

After rebuild, restore waits for the daemon then `POST /config/source` from the vault. If that fails, restore exits non-zero **without** a success banner. Fix the vault pin / network / daemon health and re-run restore, or run `config setup` once the Base is up.

### Git fetches on every reconcile / ssh config keeps disappearing

- **Branch or tag `ref:`** always refetch from upstream (only full commit SHAs short-circuit). Expected after the rebuild-persistence fix.
- **`/opt/ownbase/ssh/config` is deleted at daemon start** and never used for `GIT_SSH_COMMAND`. Per-repo Host aliases are unsupported by design (hijack vector via restore). Use the single managed key + known_hosts.

---

## Service build / start failures

### "short-name ... did not resolve to an alias and no unqualified-search registries are defined"

`podman build` failed to resolve a short base image name (e.g. `FROM golang:1-alpine`) in the service's Dockerfile — nearly every public Dockerfile uses these. `ownbased` pass zero writes a `registries.conf.d` drop-in so Podman defaults unqualified names to Docker Hub; if you see this, either the Base predates that fix or the drop-in was removed by hand:

```bash
ssh root@<base-host> 'test -f /etc/containers/registries.conf.d/999-ownbase-unqualified-search.conf && echo present || echo missing'
```

If it's missing, restart `ownbased` (`systemctl restart ownbased` on the Base) — pass zero re-writes it on every start — then re-push the config (or wait for the timer backstop) to retry the build.

---

## Lost credentials

### Lost API token

It never left the Base: `sudo cat /opt/ownbase/api-token` (root, 0600), or just re-register — `ownbasectl adopt <name> --host <host>` fetches it over SSH itself. To rotate it, `POST /token/reset` on the daemon API ([api.md](api.md)) — the daemon hot-swaps it, no restart.

---

## Multipass (local VM) issues

- **`multipass: command not found`** — install it: `brew install --cask multipass` (macOS) or see [multipass.run/install](https://multipass.run/install).
- **Launch hangs or times out** — first launch downloads an Ubuntu image; give it time. If Multipass itself is wedged: `multipass restart <name>`, or restart the Multipass daemon (macOS: `sudo launchctl kickstart -k system/com.canonical.multipassd`).
- **VM exists but `list` shows "(unregistered)"** — the VM has no matching profile (created by hand, or profile removed). `ownbasectl delete <name>` cleans up both; re-running `create <name>` asks before replacing it.
- **VM state is `Stopped`** — see [Pausing a local VM](../INSTALL.md#pausing-a-local-vm) to resume it and re-point the profile if the IP changed.

---

## Backup / restic errors

- **"--password is required"** — the restic password is the encryption key for your backup repo. After `backup setup` it is also in your vault, so `restore` can read it without re-typing. If the vault is gone too, you need the copy you saved at setup time.
- **First backup fails right after `backup setup`** — the config commit reaches the daemon asynchronously; `setup` retries the "no backup repo configured" race for 30 seconds automatically. A *persistent* failure means bad credentials or an unreachable repo — check the repo URL scheme (`s3:`, `b2:`, `sftp:`) and credentials, then `ownbasectl backup run <name>` to retry.
- **`backup status` says "not yet verified"** — the verified-restore drill runs on its own cadence (default daily), so right after setup this is normal. Rather than waiting, run `ownbasectl checkup <base> --verify` to run the drill now; it streams progress and names any check that fails. If it keeps failing, the named check is the thing to chase — `journalctl -u ownbased` on the Base has the underlying restic or Postgres output.
- **`backup prune` fails under `--append-only`** — the Base's stored cloud keys are non-deleting by design, so prune needs delete-capable credentials. Pass `--admin-aws-…` / `--admin-b2-…` (or `--creds-stdin`), or escrow them once with `backup prune --escrow` / `backup setup --admin-…`. Restic also needs `s3:DeleteObject` on the `locks/*` prefix even for append-only snapshot keys.
- **Checkup says append-only backups have not been pruned** — retention is owner-driven in that mode. Run `ownbasectl backup prune <base>` (default cadence is roughly every 30 days of retention; the finding fires after ~60 days without a prune).
- **Checkup says vault restic password does not match the Base** — the escrow and the Base secret drifted (often after `secrets set backup` on one side only, or a partial rekey). Run `ownbasectl backup rekey <base> --generate` (or the same `--new-password` you already chose) so both sides and the repository agree.
- **`restore` refuses to run** — it restores only snapshots that passed a verify drill, unless you pass `--force`. Prefer waiting for a verified snapshot when you have the choice.

---

## Postgres point-in-time recovery

- **"`<time>` is newer than the last WAL segment in the repository"** — `db restore` refused before doing anything, which is the intended outcome. Postgres archives a WAL segment when it fills or `archive_timeout` elapses, so on a quiet database the newest recoverable point trails the present by minutes and "restore to right now" asks for something the repository does not have. Use the timestamp the message names, force a segment switch first (`select pg_switch_wal();` inside the container), or omit `--to` to recover everything the repository holds.
- **"recovery ended before configured recovery target was reached"** — the same cause reaching Postgres directly, either from a restore started outside `db restore` or from a `--to` sitting at the very end of the recovery window. That end is when the last WAL segment finished archiving, which is after the last change inside it, so a target there is unreachable even though it is inside the window. It reads like data loss and is not: the data is intact and the target was simply in the future as far as the repository is concerned. Re-run with an earlier target, or with no `--to` at all.
- **`db status` says archiving is FAILING** — the database is fine and the recovery window has stopped moving; every change since the last success is currently unrecoverable. `podman logs ownbase-pgbackrest` on the Base has the reason, usually a full repository volume, an SSH key the Postgres container can no longer use, or a stanza that needs `pgbackrest stanza-create` after a restore. Nothing else about the Base will look wrong while this is true, so treat it as urgent.
- **A scratch instance is in the way** — `db restore --into scratch` leaves a container running on purpose, and a second restore replaces it. To remove it now: `podman rm -f ownbase-db-scratch` on the Base.
- **A production restore finished but the post-promote backup failed** — the database is up and serving, but it is on a new timeline that no backup covers, so it cannot be recovered again until one exists. Run `pgbackrest --stanza=main --type=full backup` inside the Postgres container, or `ownbasectl db status <base>` to confirm a full backup on the current timeline appeared.

---

## Upgrading the daemon itself

`ownbasectl upgrade` updates the **core package** (Caddy) — not `ownbased`. To update the daemon binary on a Base, install the new signed release and restart the service:

```bash
ssh root@<base-host>
ARCH=$(dpkg --print-architecture)   # amd64 or arm64
curl -fsSL -o /tmp/ownbased      https://releases.ownbase.ai/daemon/latest/ownbased-linux-$ARCH
curl -fsSL -o /tmp/ownbased.minisig https://releases.ownbase.ai/daemon/latest/ownbased-linux-$ARCH.minisig
minisign -Vm /tmp/ownbased -x /tmp/ownbased.minisig \
  -P 'RWTaLp3BlckCjjicEDrN7oVrRhGDWhSjgOpR2Ue/yHzP0cFsmmxALr/V'
install -o ownbase -g ownbase -m 0755 /tmp/ownbased /opt/ownbase/bin/ownbased
systemctl restart ownbased
```

(Replace `latest` with a pinned version like `v0.2.0` to install a specific release; the public key above is printed in [install.sh](../install.sh).) The daemon's state all lives in `/opt/ownbase` and the config repo, so replacing the binary is safe — the restart resumes the normal reconcile loop.

---

## Still stuck?

`ownbasectl checkup <name>` aggregates most health signals with the command that fixes each finding. For anything the daemon did or refused to do, the audit log on the Base (`/opt/ownbase/logs/audit.log`, newline-delimited JSON) records every action with its outcome.
