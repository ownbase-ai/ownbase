# Daemon HTTP API

> Every endpoint served by `ownbased` (`internal/explain/status.go` + `internal/explain/api.go`). This is the API `ownbasectl` calls; anything the CLI can do, any HTTP client can do.

## Reaching the API

The daemon listens on `--status-addr` (default `127.0.0.1:7070`) — **loopback only**. It is never exposed to the network. Clients reach it through an SSH tunnel; `ownbasectl` opens one automatically from the server profile. Manually:

```bash
ssh -N -L 7070:127.0.0.1:7070 root@<base-host> &
curl -H "Authorization: Bearer $(ssh root@<base-host> cat /opt/ownbase/api-token)" \
  http://127.0.0.1:7070/status
```

## Authentication

All endpoints except `GET /health` require a Bearer token:

```
Authorization: Bearer <token>
```

The token is generated at install time and stored at `/opt/ownbase/api-token` (root, 0600) on the Base. A missing or wrong token returns `401 unauthorized`. When the daemon is started with no token configured (dev only), auth is disabled.

Common status codes across endpoints:

| Code | Meaning |
|---|---|
| `401` | Missing/invalid Bearer token |
| `405` | Wrong HTTP method for the endpoint |
| `501` | The daemon was started without the capability this endpoint needs (e.g. non-Linux platform, callback not wired) |

---

## Health and status

### `GET /health` — liveness (public, no auth)

```json
{"ok": true}
```

### `GET /status` — the full BaseStatus document

The single source of truth for observability. `ownbasectl status`, `updates`, `security`, `checkup`, and `backup status` all render slices of this payload.

Schema (`schema_version: v3`):

```json
{
  "generated_at": "2026-06-19T22:00:00Z",
  "schema_version": "v3",
  "services": [
    {
      "name": "auth",
      "running": true,
      "healthy": true,
      "repo": "git@github.com:org/auth.git",
      "ref": "v1.0.0",
      "requires": ["postgres"]
    }
  ],
  "security": {
    "backup_restorable": true,
    "last_backup": "2026-06-19T21:00:00Z",
    "last_verified": "2026-06-19T06:00:00Z",
    "drift_detected": false,
    "exposure": { "...": "network exposure inventory (ss + ufw)" },
    "access": { "...": "SSH access monitor (fail2ban + journald)" },
    "vulns": { "...": "CVE scan results (trivy, host + images)" }
  },
  "updates": {
    "drift": [
      {
        "service": "auth",
        "ref": "v1.0.0",
        "commits_behind": 3,
        "newest_tag": "v1.1.0",
        "up_to_date": false
      }
    ]
  },
  "audit": {
    "total_seen": 42,
    "recent_actions": [
      { "time": "...", "action": "service.start", "target": "ownbase-auth", "outcome": "ok" }
    ]
  }
}
```

`ownbasectl updates --json` emits exactly the `updates` object; `ownbasectl security --json` emits exactly the `security` object.

---

## Core package

### `GET /core/status` — core package state (read-only)

Behind `ownbasectl upgrade` (check-only mode). Reports the core package's pinned image, digest, and running state:

```json
{
  "packages": [
    {
      "name": "Caddy",
      "container": "ownbase-core-caddy",
      "image": "docker.io/library/caddy:2.11.4-alpine",
      "digest": "sha256:…",
      "running": true
    }
  ]
}
```

### `POST /upgrade` — pull + restart the core package

Behind `ownbasectl upgrade --apply`. Pulls the latest pinned image for Caddy and restarts its container. **Streams** progress as `text/plain`; the final line is the sentinel `---OK---` on success (its absence means the upgrade failed even though the HTTP status was already committed as 200). Triggers a vulnerability rescan on completion.

---

## Security

### `POST /security/scan` — trigger an immediate CVE rescan

Returns quickly; the scan runs asynchronously (results land in `/status` within a few minutes).

```json
{"status": "started", "message": "Scan started — results available in a few minutes. Check 'ownbasectl security'."}
```

Returns `503` if the daemon is still initialising.

### `POST /security/fix` — apply host OS package patches

Behind `ownbasectl security fix`. Runs `apt-get update` + `apt-get upgrade -y` on the Base. **Streams** the apt output as `text/plain`, ending with `---OK---` on success. Triggers a vulnerability rescan on completion. Returns `501` on non-Ubuntu/Debian platforms.

---

## Config

The config repo is **external** (e.g. on GitHub). The daemon has read-only access and never writes to it — all mutations are committed client-side by `ownbasectl` (which pushes with the operator's git credentials) and applied on the Base via `POST /reconcile`. There is no `POST /config` write endpoint.

### `GET /config` — read the current ownbase.yaml

Behind `ownbasectl config get`. Returns the raw YAML document from the read-only checkout as `text/x-yaml`, not JSON. `POST /config` returns `405` (write path removed).

### `POST /reconcile` — pull the config repo and reconcile

Behind every client-side mutation (`config set`, `service *`, `deploy`, `backup setup`). Fetches the external config repo into `/opt/ownbase/checkout` (hard-reset to the tracked ref) and wakes the reconcile loop immediately.

Response: `{"status": "reconciling"}`. Returns `500` if the fetch fails, `501` if the daemon has no reconcile capability wired.

### `POST /config/source` — point the Base at its config repo

Behind `ownbasectl config setup`. Records the external config repo (`repo_url` + optional `ref`) in `/opt/ownbase/config-source.yaml`, (re)clones it read-only, and reconciles.

Request:

```json
{"repo_url": "git@github.com:org/ownbase-config.git", "ref": "main"}
```

`ref` is optional (defaults to `main`). Response: `{"status": "configured", "repo_url": "…"}`. Returns `400` when `repo_url` is empty.

### `GET /ssh-key` / `POST /ssh-key` — manage the Base's git deploy identity

Behind `ownbasectl ssh-key`. `GET` returns the Base's managed SSH public key (`{"public_key": "…"}`, empty when none exists). `POST` ensures the managed ed25519 key exists under `/opt/ownbase/ssh`, optionally records a host's SSH host keys, and returns the public key to register as a read-only deploy key.

Request (POST, body optional):

```json
{"host": "github.com"}
```

Response: `{"public_key": "ssh-ed25519 …"}`.

---

## Backups

Backup configuration (`core.backup.repo` + cadences) is committed to `ownbase.yaml` **client-side** by `ownbasectl backup setup` (the same commit path as `config set`) and applied via `POST /reconcile`; the backup scheduler picks it up within a minute — no restart. There is no `POST /backup/configure` endpoint.

Credentials are stored via the secrets API under the conventional service name `backup` (`POST /secrets/backup` with `RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, …).

### `POST /backup/run` — snapshot now

Runs one backup cycle synchronously (the daemon allows up to 10 minutes). Response:

```json
{"last_backup": "…", "latest_snapshot": "abc123", "restorable": true, "last_error": ""}
```

---

## Secrets

Secrets are age-encrypted YAML files at `/opt/ownbase/secrets/<service>.yaml.age`. The API decrypts/re-encrypts on the Base; plaintext exists only in memory on the Base and inside the SSH tunnel.

| Method + path | Behavior | Response |
|---|---|---|
| `GET /secrets/` | List services that have secrets | `{"services": ["backup", "myapp"]}` |
| `GET /secrets/{service}` | List key names (never values) | `{"service": "myapp", "keys": ["DB_URL"]}` |
| `GET /secrets/{service}/{key}` | Read one decrypted value | `{"key": "DB_URL", "value": "postgres://…"}` |
| `POST /secrets/{service}` | Merge key-value pairs (body: `{"KEY": "value", …}`); creates the file if absent | `{"service": "myapp", "updated": 2}` |
| `DELETE /secrets/{service}/{key}` | Remove one key | `{"service": "myapp", "deleted": "DB_URL"}` |

`404` when a key does not exist.

---

## Tokens

### `POST /token/reset` — rotate the API Bearer token

Generates a new token, persists it to `/opt/ownbase/api-token`, and hot-swaps it in the running daemon (no restart; the new token applies from the next request):

```json
{"token": "…new token…"}
```

Note that any stored client profiles (`~/.ownbase/config`) still hold the old token. Update a profile with `ownbasectl adopt <name> --host <host> --token <new-token>` (profiles with no stored token fetch it over SSH automatically on connect).
