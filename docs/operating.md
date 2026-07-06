# Operating a Base

> The playbook for anyone — human or AI — with SSH/CLI access to a running Base who needs to change what's deployed on it, diagnose a problem, or check its health. The reference docs ([cli.md](cli.md), [api.md](api.md), [ownbase-yaml.md](ownbase-yaml.md)) describe every surface; this page is the order of operations.

## The rules of the road

1. **Read the config repo first.** Its README (seeded at install) carries the operating contract, and `ownbase.yaml` declares every service — what it's built from, what it requires, and how it's reached. For live state (running/healthy, security posture), run `ownbasectl status <base>` or `checkup` — don't explore the machine by hand.

2. **The only way to change what's running is `ownbase.yaml` + a commit.** Never `podman run`, `systemctl edit`, or hand-edit anything under `runtime/` — those files are compiler output and get overwritten on the next reconcile. See [ownbase-yaml.md](ownbase-yaml.md) for the schema.

3. **Push to the Base's own config repo, not GitHub.** It's a local, remote-less bare repo — no hosted git server in between. `ownbasectl config set`/`service add|update|remove` push there for you, or push directly over SSH by hand; the daemon reconciles automatically (hook-triggered — seconds, not minutes). To track an external GitHub repo, declare it with `mirror:` and let the daemon manage the local mirror.

4. **Use `ownbasectl` for everything else** — config, services, status, secrets, backups, security, core-package upgrades. See [cli.md](cli.md) for the full command reference, or [api.md](api.md) to call the daemon's HTTP API directly.

5. **Updating a service = edit `ref:` and commit.** There is no other update mechanism. The daemon never opens PRs or mutates the repo on its own initiative, with one transparent exception: it resolves a blank `ref:` to a concrete commit SHA and commits that pin back.

6. **Before anything destructive** (restore, delete), check `ownbasectl backup status <base>`. The durability guarantee only holds if the last verified restore actually passed — a backup that was never restore-tested is not restorable by definition.

## Common tasks

| Task | How |
|---|---|
| See what's deployed and healthy | `ownbasectl status <base>` (declared services: `ownbase.yaml`) |
| Deploy or change a service | `ownbasectl service add/update/remove <base> <name> ...`, or edit `ownbase.yaml` and push |
| Update a service | Edit its `ref:`, commit, push |
| See what's behind | `ownbasectl updates <base>` |
| Set a secret | `ownbasectl secrets set <base> <service> KEY=value` |
| Full health check | `ownbasectl checkup <base>` |
| Diagnose a failure | `journalctl -u ownbased -f` on the Base; see [troubleshooting.md](troubleshooting.md) |
| Audit what the daemon did | `/opt/ownbase/logs/audit.jsonl` on the Base |

## When something is broken

Start with [troubleshooting.md](troubleshooting.md) — it is organized by symptom. The daemon journal (`journalctl -u ownbased -f`) is the single most useful diagnostic for anything happening on the Base itself.
