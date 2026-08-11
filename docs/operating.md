# Operating a Base

> The playbook for anyone — human or AI — with SSH/CLI access to a running Base who needs to change what's deployed on it, diagnose a problem, or check its health. The reference docs ([cli.md](cli.md), [api.md](api.md), [ownbase-yaml.md](ownbase-yaml.md)) describe every surface; this page is the order of operations.

## The rules of the road

1. **Read the config repo first.** The **external** config repo (e.g. on GitHub) holds `ownbase.yaml`, which declares every service — what it's built from (`repo:`), what it requires, and how it's reached. For live state (running/healthy, security posture), run `ownbasectl status <base>` or `checkup` — don't explore the machine by hand.

2. **The only way to change what's running is `ownbase.yaml` + a commit on the tracked ref (after merge).** Never `podman run`, `systemctl edit`, or hand-edit anything under `runtime/` — those files are compiler output and get overwritten on the next reconcile. See [ownbase-yaml.md](ownbase-yaml.md) for the schema.

3. **Config lives in an external git repo; two mutation paths.** Operators use `ownbasectl config set`/`service add|update|remove`/`deploy`/`backup setup` — clone, edit, commit to the **tracked ref** with **your** git credentials, then `POST /reconcile`. Agents with `config:write` may `POST /config` to push an `ownbase/agent/*` **proposal branch** only; merge on the forge, then reconcile. Service repos stay read-only deploy keys; the config repo needs write only for the agent path. See [identity-and-authority.md](foundation/identity-and-authority.md).

4. **Use `ownbasectl` for everything else** — config, services, status, secrets, backups, security, core-package upgrades. See [cli.md](cli.md) for the full command reference, or [api.md](api.md) to call the daemon's HTTP API directly (TCP Bearer or in-container socket).

5. **Never use plain `ssh`. Use `ownbasectl ssh <base>`.** Two reasons, and both matter more for an agent than for a human. There is no key file to find: the owner key lives in the [vault](vault.md) and the credential agent signs with it, so nothing you run ever holds a private key. And every session is recorded to `~/.ownbase/sessions/` in asciicast v2 format, which is how the owner sees what you actually did on a machine you have root on. A shell opened outside `ownbasectl ssh` leaves no trail and should be treated as a mistake. Read the trail back with `ownbasectl sessions list` and `sessions show <id>`.

6. **Moving a service to new code = `ownbasectl deploy`.** `deploy` resolves the requested ref to a concrete commit SHA and commits it to the tracked ref; branch-named refs never auto-redeploy. Agents do not bump `ref:` themselves unless they propose a full document via `POST /config` and it is merged.

7. **Before anything destructive** (restore, delete), check `ownbasectl backup status <base>`. The durability guarantee only holds if the last verified restore actually passed — a backup that was never restore-tested is not restorable by definition.

8. **A vault/Base config-repo mismatch is a security signal.** If `status`/`checkup` warn that the Base reports a different config URL than the vault pins, do **not** treat it as automatic sync. Intentional repoint: `ownbasectl config setup`. Unintentional: investigate the Base.

## Common tasks

| Task | How |
|---|---|
| See what's deployed and healthy | `ownbasectl status <base>` (declared services: `ownbase.yaml`) |
| Add or change a service | `ownbasectl service add/update/remove <base> <name> ...` |
| Let a service call the daemon API | `ownbasectl service update <base> <name> --ownbase-access status:read` (or edit `ownbase_access:`; see [api.md](api.md#service-access-over-unix-sockets-ownbase_access)) |
| Propose config from an agent | `POST /config` with `config:write` → merge PR on forge → `POST /reconcile` |
| Deploy / update a service to a ref | `ownbasectl deploy <base> <name> --ref <ref>` |
| Scale a worker pool | `ownbasectl service update <base> <name> --replicas N` (concurrency on one machine, not HA; see [ownbase-yaml.md](ownbase-yaml.md#replicas-replicas)) |
| Inspect one worker over tunnel | `ownbasectl tunnel <base>` — for a replicated service this is **replica 0** only |
| See what's behind | `ownbasectl updates <base>` |
| Set a secret | `ownbasectl secrets set <base> <service> KEY=value` (one file shared by all replicas of that service) |
| Full health check | `ownbasectl checkup <base>` |
| Get a shell on the Base | `ownbasectl ssh <base>` (recorded; never plain `ssh`) |
| Run one command on the Base | `ownbasectl ssh <base> -- <command>` |
| Diagnose a failure | `ownbasectl ssh <base> -- journalctl -u ownbased -n 200`; see [troubleshooting.md](troubleshooting.md) |
| Audit what the daemon did | `/opt/ownbase/logs/audit.log` on the Base (newline-delimited JSON; includes principal) |
| Audit what *you* did | `ownbasectl sessions list`, then `sessions show <id>` |

### Agent config-proposal loop

1. Service has `ownbase_access: [config:write]` (and ideally not `*`).
2. Config repo deploy key has **write**; default branch is protected.
3. Agent `POST /config` with full validated YAML → branch `ownbase/agent/…`.
4. Human (or forge auto-merge rule) merges the PR.
5. Agent or operator `POST /reconcile` (scope `reconcile`) applies the tracked ref.

A service principal **cannot** call owner-only routes (`/config/source`, `/token/reset`, `/self-update`, `/security/*`, `/db/restore`, …) even with `*`. A `403` on those paths is by design.

## When something is broken

Start with [troubleshooting.md](troubleshooting.md) — it is organized by symptom. The daemon journal is the single most useful diagnostic for anything happening on the Base itself; reach it with `ownbasectl ssh <base> -- journalctl -u ownbased -n 200`, or `ownbasectl ssh <base>` and then `journalctl -u ownbased -f` if you want to follow it.

If `ownbasectl` itself refuses to run — "the vault is locked", "no vault configured" — the problem is on your machine, not the Base. See [vault.md](vault.md).
