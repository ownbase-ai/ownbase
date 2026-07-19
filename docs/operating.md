# Operating a Base

> The playbook for anyone — human or AI — with SSH/CLI access to a running Base who needs to change what's deployed on it, diagnose a problem, or check its health. The reference docs ([cli.md](cli.md), [api.md](api.md), [ownbase-yaml.md](ownbase-yaml.md)) describe every surface; this page is the order of operations.

## The rules of the road

1. **Read the config repo first.** The **external** config repo (e.g. on GitHub) holds `ownbase.yaml`, which declares every service — what it's built from (`repo:`), what it requires, and how it's reached. For live state (running/healthy, security posture), run `ownbasectl status <base>` or `checkup` — don't explore the machine by hand.

2. **The only way to change what's running is `ownbase.yaml` + a commit.** Never `podman run`, `systemctl edit`, or hand-edit anything under `runtime/` — those files are compiler output and get overwritten on the next reconcile. See [ownbase-yaml.md](ownbase-yaml.md) for the schema.

3. **Config lives in an external git repo; mutations are client-side.** `ownbasectl config set`/`service add|update|remove`/`deploy`/`backup setup` clone the config repo, edit `ownbase.yaml`, commit, and push with **your** git credentials, then trigger a reconcile via `POST /reconcile`. The Base has read-only access. Every service is built from an external git repo declared with `repo:`; private repos use the Base's deploy key (`ownbasectl ssh-key`).

4. **Use `ownbasectl` for everything else** — config, services, status, secrets, backups, security, core-package upgrades. See [cli.md](cli.md) for the full command reference, or [api.md](api.md) to call the daemon's HTTP API directly.

5. **Moving a service to new code = `ownbasectl deploy`.** There is no other update mechanism. `deploy` resolves the requested ref to a concrete commit SHA and commits it to the config repo; branch-named refs never auto-redeploy, and the daemon never mutates the repo on its own initiative.

6. **Before anything destructive** (restore, delete), check `ownbasectl backup status <base>`. The durability guarantee only holds if the last verified restore actually passed — a backup that was never restore-tested is not restorable by definition.

## Common tasks

| Task | How |
|---|---|
| See what's deployed and healthy | `ownbasectl status <base>` (declared services: `ownbase.yaml`) |
| Add or change a service | `ownbasectl service add/update/remove <base> <name> ...` |
| Deploy / update a service to a ref | `ownbasectl deploy <base> <name> --ref <ref>` |
| See what's behind | `ownbasectl updates <base>` |
| Set a secret | `ownbasectl secrets set <base> <service> KEY=value` |
| Full health check | `ownbasectl checkup <base>` |
| Diagnose a failure | `journalctl -u ownbased -f` on the Base; see [troubleshooting.md](troubleshooting.md) |
| Audit what the daemon did | `/opt/ownbase/logs/audit.jsonl` on the Base |

## When something is broken

Start with [troubleshooting.md](troubleshooting.md) — it is organized by symptom. The daemon journal (`journalctl -u ownbased -f`) is the single most useful diagnostic for anything happening on the Base itself.
