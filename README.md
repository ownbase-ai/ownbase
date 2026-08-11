# OwnBase

> Build faster with AI. Own everything.

**AI builds faster when it owns the whole machine.**

A coding agent can write an application in an afternoon, then lose the rest of the week to the parts it cannot see through: a provider console, a managed database's dashboard, a deploy service's API, a secrets store, a billing account. Behind each of those it can call the endpoints a vendor chose to expose and nothing else — it cannot read the log, restart the process, or check whether what it just did worked.

A **Base** is one machine you own where none of that is true.

```text
Without a Base                    With a Base

  agent                             agent
    ├── deploy platform               │
    ├── managed database              ▼
    ├── DNS + TLS panel            one machine you own
    ├── object storage                │
    ├── secrets manager               ▼
    └── a console for each         everything
```

OwnBase is open-source software that turns a machine you rent or own into that Base: a hardened, self-maintaining home for everything you build. A daemon on the machine does the firewall, the patches, the certificates, the backups, and the recovery, and hands you the keys to all of it.

Your server. Your software. Your copy of OwnBase. No subscriptions, no platform in the middle.

## Why build here

**Your AI gets a real computer, not a tenancy.** Behind a platform API, an AI agent is a guest. On a Base it has the actual machine. It can read logs, inspect processes, run any open-source software, stand up its own Postgres, and check whether what it just did actually worked. Most of what makes agents unreliable in deployment is missing feedback and missing permission. A Base removes both, which is the entire speed argument: the work lands in one session instead of coming back as a question.

**You own all of it, including OwnBase.** Config is plain files in a Git repo you control. Every service is built from source, so there is no image registry to lose access to. Secrets decrypt on your machine with a key that never leaves it. OwnBase itself is [MIT-licensed](LICENSE) — fork it, change it, run your own build. Uninstalling leaves a working, still-hardened Ubuntu box behind ([docs/uninstall.md](docs/uninstall.md)).

**Backups are proven, not promised.** Encrypted off-machine snapshots on a schedule, plus a recurring drill that *actually restores* the latest snapshot and verifies it. `ownbasectl checkup` reports whether the Base is provably restorable, not merely "backed up".

**One machine replaces a pile of subscriptions.** Auth, databases, job queues, cron, and every app you build share one modest box and one predictable bill — and one place for an agent to reason about, instead of six integrations to wire together.

**Owning software used to be the expensive half.** Creating software got cheap; keeping it alive did not. Someone still has to secure it, patch it, renew its certificates, back it up, and recover it at 2 a.m. That is the work a Base absorbs, and the reason OwnBase exists — see [MISSION.md](MISSION.md).

---

## Setting up a Base

This section is written to be executed top to bottom, by a person or by an AI agent working on their behalf. It takes about ten minutes, most of which is waiting.

There is exactly one step a human must do, because server providers require a human with a credit card: **creating the machine**. Everything on either side of that is a command.

### 1. Install the app or the CLI

Both run on your own computer, not on the server. Pick whichever you prefer — they share one vault and cannot disagree about what is deployed.

```bash
brew install --cask ownbase-ai/tap/ownbase     # app — macOS only
brew install --cask ownbase-ai/tap/ownbasectl  # CLI — macOS or Linux
```

The app walks through this whole section with a wizard and already bundles `ownbasectl`. The CLI is what an agent will use, and what the rest of this walkthrough shows.

Linux desktop app (`.deb` / AppImage), or CLI without Homebrew: [docs/desktop.md](docs/desktop.md#installing-it) and [INSTALL.md](INSTALL.md#install-ownbasectl).

### 2. Create the vault

Everything that reaches a Base — SSH keys, API tokens, hosts — goes in one encrypted file that you own and put wherever you like.

```bash
ownbasectl vault init ~/Dropbox/OwnBase
```

It asks for a master password. Pick a folder your cloud storage syncs: the file is a standard [KDBX](https://keepass.info/help/kb/kdbx.html) database, useless to anyone without the password, and having it on a second machine is what saves you if this one dies. [vault.md](docs/vault.md) covers the format and how to open it with KeePassXC.

Then unlock it for this session:

```bash
ownbasectl vault unlock
```

> **Agent:** ask the user to run both of these themselves. The master password is the one secret that should never pass through you.

### 3. Create the SSH key

A provider authorizes an SSH key when the machine is created, so the key has to exist first.

```bash
ownbasectl keygen mybase
```

This puts a new key in your vault and prints the public half. Re-running it is safe — an existing key is printed, never replaced. Each Base gets its own key, so retiring one Base revokes exactly one credential. There is no private key file anywhere on disk; when something needs to authenticate, the credential agent signs for it.

### 4. Create the server

This is the human step. In your provider's console, create a machine that is:

- **Ubuntu 24.04** (22.04 also works)
- **at least 2 GB RAM and 20 GB disk** — see [sizing](#how-big-a-machine) below
- created with **the public key from step 3 pasted into the provider's "SSH key" field**
- reachable as root over SSH, which is the default on nearly every provider image

Any provider works: Hetzner, OVHcloud, DigitalOcean, Vultr, Scaleway, or a machine in a closet. OwnBase has no provider integration and needs none.

The key must be pasted **when the machine is created**. Most providers cannot add one to an existing machine without rebuilding it.

When it finishes booting, note its **IP address**.

> **Agent:** you cannot do this step — it needs a human with a payment method. Show them the public key from step 3, the four requirements above, and ask for the IP address when the machine is up.

### 5. Install OwnBase

```bash
ownbasectl create mybase --remote root@<ip> --wait
```

One command, unattended, no prompts. It waits for the new server to start accepting SSH, checks the machine is fit before changing anything on it, installs and signature-verifies the daemon, hardens the host (Podman, UFW, fail2ban, automatic security updates, CVE scanning), and registers the Base locally so every other command works immediately.

`--wait` blocks until hardening has actually finished. Without it the command returns a minute or two early, while the daemon is still working.

Add `--caddy-email you@example.com` if services will be on public domains — it is the ACME contact for automatic TLS certificates. Add `--json` for machine-readable output.

A fresh Base exposes nothing but SSH, so there is nothing to secure yet and no rush to the next step.

### 6. Point the Base at a config repo

`ownbase.yaml` is the single source of truth for what runs on the Base. It lives in a **Git repo you own** (on GitHub or anywhere else). Operators commit to the tracked ref from your machine with your credentials; agents may push proposal branches (`ownbase/agent/*`) via the daemon after you grant write on the config repo.

Give the Base a deploy key first:

```bash
ownbasectl ssh-key add mybase --host github.com
```

Register the printed key:

| Repo | Permission | Why |
|---|---|---|
| Each **service** repo the Base builds from | **Read-only** deploy key | Clone and build only |
| The **config** repo | **Read-only** by default; **write** only if agents should call `POST /config` | Tracked ref stays protected by branch protection on `main` |

(This is a human step — it needs access to the repo's settings.) Then:

```bash
ownbasectl config setup mybase --repo git@github.com:you/mybase-config.git --init
ownbasectl config get mybase
```

`--init` seeds an empty repo with a working starter config: Postgres 17 with point-in-time recovery, plus the pgBackRest repository host that owns its WAL archive. The database password and its SSH keypair are generated by the Base itself on the first reconcile. Delete both services if this Base needs no database.

### 7. Turn on backups

Do this before there is data worth losing.

```bash
ownbasectl backup setup mybase \
  --repo s3:s3.amazonaws.com/my-bucket/ownbase \
  --password <a-strong-password> \
  --aws-access-key-id AKIA... --aws-secret-access-key ...
```

Takes the first snapshot immediately, then schedules hourly snapshots and a daily verified-restore drill. B2 and SFTP repositories work too.

**Save that password somewhere durable.** Nothing can recover it for you — no daemon, no project, no vendor. That is precisely what makes the backups yours alone.

### 8. Deploy something

```bash
ownbasectl service add mybase hello \
  --repo https://github.com/traefik/whoami \
  --port 80 --domain hello.example.com \
  --add-capabilities NET_BIND_SERVICE

ownbasectl deploy mybase hello --ref master
ownbasectl status mybase
```

`service add` declares the service; `deploy` resolves the ref to a commit, pins it in the config repo, and asks the Base to build and start it, health-gated. The daemon clones the repo and builds it from source on the Base — there is no image registry anywhere in this.

`--add-capabilities` appears here only because every container starts with all Linux capabilities dropped and `whoami` binds port 80, a privileged port. Most services listen on 3000 or 8080 and never need it.

No DNS yet? `ownbasectl tunnel mybase` serves the Base's services at trusted local HTTPS URLs over SSH, which works offline and needs no `/etc/hosts` entry.

---

## How big a machine

Services cost almost nothing at rest. What sets the floor is that **every service is built from source on the Base**, and a build's peak is far larger than the container it produces.

Measured on a 2 vCPU / 2 GB Ubuntu 24.04 Base:

| | Idle memory |
|---|---|
| Ubuntu + OwnBase daemon + Caddy | ~265 MB |
| A compiled service (Go) | ~1.5 MB |
| A Node service | ~8 MB |
| Postgres 17 | ~4 MB |

| Build | Peak above idle | Time |
|---|---|---|
| Go service, multi-stage | ~360 MB | 45 s |
| Next.js app (`npm install` + build) | ~460 MB | 75 s |

So the rule is: **~300 MB for the system, ~500 MB of headroom for the single largest build, and then your services**, which are cheap until they take traffic.

| Machine | Comfortable for |
|---|---|
| 2 GB RAM, 20 GB disk | The floor. A few small apps and a database. Builds succeed, one at a time. |
| 4 GB RAM, 40 GB disk | Where most people should start. Roughly a dozen apps plus Postgres doing real work. |
| 8 GB RAM, 80 GB disk | Dozens of apps and several databases, without thinking about it. |

Disk fills faster than memory. Bare Ubuntu is about 2 GB; add OwnBase, Caddy, and a first service and you are near 4 GB; each additional build toolchain (a Go builder image, a Node one) caches roughly another gigabyte of layers.

None of this is a commitment. Providers resize machines, and `ownbasectl restore` rebuilds a Base onto a bigger one from its backups. Start small.

---

## Operating a Base

**There is exactly one way to change what runs: commit to `ownbase.yaml` in the config repo.** `ownbasectl` does that for you and tells the Base to reconcile. Nothing on the Base is hand-edited, which is why the Base can always be rebuilt from the repo, the secrets, and the backups.

The commands worth knowing:

| Command | What it does |
|---|---|
| `ownbasectl status <base>` | What is deployed, what is healthy, what the security posture is (`--json` for the full payload) |
| `ownbasectl checkup <base>` | One plain-language health report: intrusions, exposure, CVEs, update drift, backup health, each with its fix |
| `ownbasectl deploy <base> <svc> --ref <ref>` | The only way to move a service to a new version |
| `ownbasectl ssh <base>` | A shell on the Base. Recorded, replayable, and the only shell you should open |
| `ownbasectl sessions list` | Every recorded session — what was run on your machines and by whom |
| `ownbasectl tunnel <base>` | Reach any service at a trusted local HTTPS URL over SSH |
| `ownbasectl backup status <base>` | Last snapshot, last verified drill, and whether it is genuinely restorable |
| `ownbasectl restore <base> --repo <url> --password <pw>` | Rebuild the whole Base onto a fresh machine |

Everything else — secrets, databases, updates, multiple Bases, uninstalling — is in [docs/cli.md](docs/cli.md).

**Use `ownbasectl ssh` rather than plain `ssh`.** It signs with the key in your vault without ever handing it over, and it records the session so you can see exactly what happened on a machine you have given an agent root on. That recording is what makes the arrangement safe.

Run `checkup` weekly. It is the one command that answers "is anything wrong?" in a sentence.

---

## Documentation

- [MISSION.md](MISSION.md): why OwnBase exists, the promise, and the six hard constraints every change respects.
- [INSTALL.md](INSTALL.md): install reference — local VM, non-Homebrew install, non-standard SSH ports, what to do when setup fails.
- [AGENTS.md](AGENTS.md): dispatch for AI agents — which document owns which job.
- [docs/vault.md](docs/vault.md): the encrypted file holding every credential — where it lives, how unlocking works, and how to open it without OwnBase.
- [docs/desktop.md](docs/desktop.md): the OwnBase app — guided setup, the dashboard, and session replay.
- [docs/operating.md](docs/operating.md): the order of operations for working on a running Base.
- [docs/cli.md](docs/cli.md): every `ownbasectl` command, flag, and default.
- [docs/ownbase-yaml.md](docs/ownbase-yaml.md): the `ownbase.yaml` schema, the `ref:` update model, secrets, databases, jobs.
- [docs/integration-contract.md](docs/integration-contract.md): the contract any service must meet to run on a Base.
- [docs/api.md](docs/api.md): the daemon's HTTP API — auth, endpoints, request and response shapes.
- [docs/troubleshooting.md](docs/troubleshooting.md): symptom-first fixes, including Postgres point-in-time recovery.
- [docs/uninstall.md](docs/uninstall.md): export everything and remove OwnBase, leaving a working Ubuntu machine.
- [docs/foundation/](docs/foundation/): the durable rules of how a Base works — read once, in order.
- [docs/decisions.md](docs/decisions.md): locked technical decisions, and why the code is the way it is.
- [docs/development.md](docs/development.md): building, testing, and the invariants to preserve when changing OwnBase itself.
- [LICENSE](LICENSE): MIT. Your copy of OwnBase is yours to read, change, fork, and ship.
