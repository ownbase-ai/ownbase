# Troubleshooting

> What to do when something fails. Each section starts from the symptom you'd actually see.

The single most useful diagnostic for anything happening *on* the Base is the daemon journal:

```bash
ssh root@<base-host> journalctl -u ownbased -f     # remote server
multipass exec <name> -- journalctl -u ownbased -f # local VM
```

---

## `create` / install failures

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

`ownbasectl` verifies host keys against `~/.ownbase/known_hosts` (trust-on-first-use). A mismatch means the machine at that address presents a different key than the one recorded — either you re-provisioned the server (likely) or something is intercepting the connection (worth ruling out). If you re-provisioned:

```bash
# remove the stale line for the host, then reconnect (the new key is re-added on first use)
grep -v '<host>' ~/.ownbase/known_hosts > /tmp/kh && mv /tmp/kh ~/.ownbase/known_hosts
```

### "ssh: unable to authenticate" / connection refused

- Check the profile: `ownbasectl list` shows host, and `~/.ownbase/config` holds `ssh_user`, `ssh_key`, and `ssh_port` per profile. Remote installs connect as `root` by default; local VMs use `ubuntu`.
- Confirm the key works outside ownbasectl: `ssh -i ~/.ssh/id_ed25519 root@<host>`.
- If sshd listens on a non-standard port, set `ssh_port` in the profile (or `--ssh-port` at create time so UFW allows it).

### "unauthorized — check that your token is correct"

The API token in your profile no longer matches the Base (e.g. someone ran `POST /token/reset`). Fetch the current token and update the profile:

```bash
ssh root@<host> sudo cat /opt/ownbase/api-token
ownbasectl adopt <name> --host <host> --token <token>
```

---

## Lost credentials

### Lost API token

It never left the Base: `sudo cat /opt/ownbase/api-token` (root, 0600). Re-register with `ownbasectl adopt` as above. To rotate it, `POST /token/reset` on the daemon API ([api.md](api.md)) — the daemon hot-swaps it, no restart.

---

## Multipass (local VM) issues

- **`multipass: command not found`** — install it: `brew install --cask multipass` (macOS) or see [multipass.run/install](https://multipass.run/install).
- **Launch hangs or times out** — first launch downloads an Ubuntu image; give it time. If Multipass itself is wedged: `multipass restart <name>`, or restart the Multipass daemon (macOS: `sudo launchctl kickstart -k system/com.canonical.multipassd`).
- **VM exists but `list` shows "(unregistered)"** — the VM has no matching profile (created by hand, or profile removed). `ownbasectl delete <name>` cleans up both; re-running `create <name>` asks before replacing it.
- **VM state is `Stopped`** — see [Pausing a local VM](../INSTALL.md#pausing-a-local-vm) to resume it and re-point the profile if the IP changed.

---

## Backup / restic errors

- **"--password is required"** — the restic password is the encryption key for your backup repo. OwnBase cannot recover it. Store it in a password manager the moment you choose it.
- **First backup fails right after `backup setup`** — the config commit reaches the daemon asynchronously; `setup` retries the "no backup repo configured" race for 30 seconds automatically. A *persistent* failure means bad credentials or an unreachable repo — check the repo URL scheme (`s3:`, `b2:`, `sftp:`) and credentials, then `ownbasectl backup run <name>` to retry.
- **`backup status` says "not yet verified"** — the verified-restore drill runs on its own cadence (default daily). Right after setup this is normal; if it never flips to "restorable", check `journalctl -u ownbased` for restic errors.
- **`restore` refuses to run** — it restores only snapshots that passed a verify drill, unless you pass `--force`. Prefer waiting for a verified snapshot when you have the choice.

---

## Upgrading the daemon itself

`ownbasectl upgrade` updates the **core package** (Caddy) — not `ownbased`. To update the daemon binary on a Base, install the new signed release and restart the service:

```bash
ssh root@<base-host>
ARCH=$(dpkg --print-architecture)   # amd64 or arm64
curl -fsSL -o /tmp/ownbased      https://releases.ownbase.ai/daemon/latest/ownbased-linux-$ARCH
curl -fsSL -o /tmp/ownbased.minisig https://releases.ownbase.ai/daemon/latest/ownbased-linux-$ARCH.minisig
minisign -Vm /tmp/ownbased -x /tmp/ownbased.minisig \
  -P 'RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3'
install -o ownbase -g ownbase -m 0755 /tmp/ownbased /opt/ownbase/bin/ownbased
systemctl restart ownbased
```

(Replace `latest` with a pinned version like `v0.2.0` to install a specific release; the public key above is printed in [install.sh](../install.sh).) The daemon's state all lives in `/opt/ownbase` and the config repo, so replacing the binary is safe — the restart resumes the normal reconcile loop.

---

## Still stuck?

`ownbasectl checkup <name>` aggregates most health signals with the command that fixes each finding. For anything the daemon did or refused to do, the audit log on the Base (`/opt/ownbase/logs/audit.jsonl`) records every action with its outcome.
