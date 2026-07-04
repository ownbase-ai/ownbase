# Uninstalling OwnBase — retiring a Base

> Export and Retire are first-class lifecycle stages ([foundation/base-lifecycle.md](foundation/base-lifecycle.md), stages 9–10). Because a Base is just Ubuntu you own, retiring OwnBase leaves a working system behind — nothing is held hostage.

There are three levels, from gentlest to most complete:

1. **Forget the Base locally** — remove the client profile, leave the Base running.
2. **Export everything** — take code, data, and backups out in standard formats.
3. **Remove OwnBase from the machine** — uninstall the daemon and services, keep (or destroy) the machine.

---

## 1. Forget the Base locally

```bash
ownbasectl delete <name> --keep-vm   # removes only the profile from ~/.ownbase/config
```

For a local VM, plain `ownbasectl delete <name>` also destroys the VM (it asks first). A profile known to be a remote server is never destroyed by `delete` — only its local profile is removed.

---

## 2. Export everything

Export is always available — the source of truth already lives in your repos and your data is in open formats. Nothing here requires OwnBase to still be running afterwards.

**Code and config.** Every repo lives on the Base's Forgejo. Clone what you want to keep:

```bash
git clone ssh://git@<forgejo-host>/ownbase/ownbase.git      # the config repo (ownbase.yaml)
git clone ssh://git@<forgejo-host>/services/<name>.git      # each source-built service
```

**Service data.** Data lives in Podman volumes named `ownbase-<service>-<volume>` (`ownbase-<service>-data` for the single-volume shorthand). Export any of them on the Base:

```bash
podman volume ls | grep ownbase
podman volume export ownbase-myapp-data -o myapp-data.tar
```

**Secrets.** Read them out through the CLI while the daemon is still up (`ownbasectl secrets list <name>` / `get`), or keep the encrypted files plus the key: `/opt/ownbase/secrets/*.yaml.age` and `/opt/ownbase/age/key.age`.

**Backups.** Your restic repository (S3/B2/SFTP) is already yours — it is credentialed with your keys and readable by stock restic (`restic -r <repo> snapshots`). It contains the repos, secrets, age key, and volume data; holding the repo URL + restic password is a complete export by itself.

---

## 3. Remove OwnBase from the machine

Run as root on the Base. Order matters: services before runtime, runtime before user.

```bash
# 1. Stop the daemon
systemctl disable --now ownbased
rm /etc/systemd/system/ownbased.service
systemctl daemon-reload

# 2. Stop and remove the managed services (user services + Forgejo + Caddy)
podman ps -a --format '{{.Names}}' | grep '^ownbase-' | xargs -r podman rm -f
rm -f /etc/containers/systemd/ownbase-*
systemctl daemon-reload

# 3. Remove volumes — ONLY after exporting what you need (step 2 above)
podman volume ls --format '{{.Name}}' | grep '^ownbase-' | xargs -r podman volume rm
podman network ls --format '{{.Name}}' | grep '^ownbase' | xargs -r podman network rm

# 4. Remove the OwnBase state directory and system user
rm -rf /opt/ownbase
userdel ownbase
```

Then, on your own machine, remove the profile: `ownbasectl delete <name> --keep-vm`.

### What stays behind — on purpose

Pass-zero hardening is left in place, because removing it would make the machine *less* safe the moment OwnBase leaves:

- **UFW** (firewall) with its rules, **fail2ban**, **unattended-upgrades** — a still-hardened Ubuntu.
- **Podman** and **trivy** — ordinary apt packages; `apt-get remove podman trivy` if you want them gone.

The result is exactly what the constitution promises: a working, secured Ubuntu machine, all your data exported or still on disk where you put it, and no OwnBase component required for any of it.

### Destroying the machine instead

If the machine itself is being decommissioned (cloud instance deletion, VM teardown), you only need levels 1–2: export what you keep, `ownbasectl delete <name>`, then delete the machine at the provider. Your restic repository and cloned repos are sufficient to rebuild the entire Base later with `ownbasectl restore`.
