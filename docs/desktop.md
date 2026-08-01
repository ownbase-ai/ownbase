# The OwnBase app

> A window onto your Bases. It sets one up with a guided wizard, shows you the health of the ones you have, and replays every shell session that was ever opened on them.

Everything here is also available from `ownbasectl` — the app runs that same binary and renders what comes back. Use whichever you prefer; they cannot disagree, because there is only one of them. If you are an AI agent, use [the CLI](cli.md): it is the same capabilities without a window in the way.

Working on the app itself is documented in [desktop/README.md](../desktop/README.md).

## Installing it

Download the release for your platform and drag it in. On macOS the first launch needs a right-click → Open, because the app is not notarized yet.

The app bundles `ownbasectl`, so there is nothing else to install. If you also want the CLI on your `PATH` — worth it, since it is what an agent will use — see [INSTALL.md](../INSTALL.md#install-ownbasectl).

## First launch: the vault

The app opens on a single question: where do you want your credentials to live?

That file is a [KDBX vault](vault.md) — the KeePass format — holding one entry per Base and one SSH key per Base. It is encrypted with a master password you choose, and it is the one thing in OwnBase that cannot be regenerated from anything else, so where it lives is a decision worth making deliberately:

- **A synced folder** (iCloud Drive, Dropbox, Syncthing) is the recommended choice. Your provider only ever holds ciphertext, and a second copy is what survives this laptop being stolen.
- **Anywhere local** is fine if you back it up yourself.

Pick a strong master password and put it in whatever password manager you already trust. Nothing can recover it — there is no reset, no recovery code, and no one holding a copy. That is the property that makes the vault worth having, and it cuts exactly one way.

If you already have a vault — from another machine, or from the CLI — point the app at the file and it adopts it.

## Setting up a Base

**Set up a Base** in the sidebar starts with one choice: a new remote server, a local VM on this computer, or a server that already runs OwnBase.

### A new server

The common path — four steps, about ten minutes, most of it waiting.

1. **Name it.** How you will refer to the machine from here on, in the app and in commands like `ownbasectl status mybase`. It stays on your computer; the server never learns it.
2. **Your key.** The app generates an ed25519 key into your vault and shows you the public half. The private half never touches disk. If you'd rather use a key you already have, choose *I already have a key* and pick the private key file — its comment and any provider console it's already pasted into stay yours to manage. Copy the public key now — the next step needs it.
3. **The server.** This is the step the app cannot do for you: providers need a human with a payment method, and OwnBase has no provider integration to skip it with. Create an Ubuntu 24.04 machine with at least 2 GB RAM and 20 GB disk, paste your public key into the provider's *SSH key* field **as you create it** (most providers cannot add one afterwards without a rebuild), and bring back the IP address.
4. **Install.** The app runs `ownbasectl create` and streams the output. Roughly ten minutes: hardening the host, installing the daemon, bringing up Caddy. Nothing to answer.

This path is a front end for `keygen` and `create`. Doing it in a terminal instead is [the same four steps](../README.md#setting-up-a-base).

### A local VM on this computer

For trying OwnBase without a cloud bill. Needs [Multipass](https://canonical.com/multipass) installed. Three steps: name it, generate (or import) a key, and install — the app runs `ownbasectl create` with no `--remote`, Multipass launches Ubuntu, and OwnBase installs inside the VM. Nothing is exposed on the public internet. First launch may download an image.

### A server that already runs OwnBase

For a machine someone else provisioned, or one you're registering on a second computer that shares your vault. This is a front end for `ownbasectl adopt`, and it's fast — seconds, not minutes, because there's no install to run.

1. **Name it.** Same as above.
2. **Your key.** No key is generated here: the server already trusts a specific key, so the only option is to pick that private key's file. Nothing is written to your vault yet — the key is only committed once the next step proves it actually works.
3. **The server.** Its host, SSH login user, and port. There's no provider console step and no TLS email — the server is already configured.
4. **Register.** The app verifies SSH connectivity with the key you picked, fetches the API token over SSH automatically, and saves the profile. If verification fails, nothing is written — a mistyped host costs you nothing.

### How big a machine

The floor is set by building, not by running: each service is built from source on the Base, and a build peaks far above the container it produces. [Sizing guidance](../README.md#how-big-a-machine) has the numbers.

## The dashboard

Pick a Base in the sidebar. Each tab is a section of one `ownbasectl checkup` — one call, one SSH tunnel.

**Overview** leads with anything worth your attention — and only things you can finish. Each finding carries a typed action from the CLI: a button that runs the fix (apply host patches, rescan, reboot), a button that opens the relevant tab, or the command itself when the fix changes desired state. Unfixed CVEs and other readings are not listed here; they live on their tabs. Below that: the machine's identity and a summary (services running, last backup, whether that backup has ever been *proven* restorable, disk, certificate expiry). At the bottom, *Remove from this computer* forgets the Base on this laptop (vault profile and owner key). For a local Multipass VM you can also destroy the VM. It never uninstalls OwnBase on a remote server or destroys a cloud instance — that is your provider's console, or the steps in [uninstall.md](uninstall.md).

**Services** shows what `ownbase.yaml` asks for beside what the machine actually has running — the deployed ref, the domains, and the result of the health probe. A service can be running and unhealthy, and that reads differently from running.

**Security** has five parts: whether a reboot is required for applied packages to take effect; which ports the machine believes are reachable and whether each one is expected; who got in over SSH and who kept failing to; known CVEs in the host packages and in each service image (expand a row to see the top findings; only CVEs with a published patch are actionable); and whether any generated file on the Base has drifted from what the compiler produced. *Rescan* triggers an immediate trivy pass. *Reboot now* appears only when the host says it needs one.

The exposure list is the machine's own view. It cannot see a firewall your provider runs in front of it, and it cannot see a socket a compromised kernel is hiding from it. It is one input, not a verdict.

**Backups** is where the restore drill lives. *Back up now* takes a snapshot. *Run the restore drill* is the one that matters: the Base restores its newest snapshot into an isolated directory, checks it, and when Postgres is in the backup starts a real database from it and waits for recovery. Until that has passed, "restorable" is an assumption — which is why the app says *not yet verified* rather than showing you a green light you did not earn.

**Updates** shows how far each service is from its source repo. Nothing updates itself. Moving one forward is `ownbasectl deploy`, which resolves the ref to a concrete commit and commits it to your config repo, so what is deployed stays written down.

**Activity** is the Base's own audit log: every governed action it took, newest first.

### What the app will not do

It will not edit `ownbase.yaml`. Your config lives in a Git repo you own, every change to it is a commit, and the app is not going to make commits you did not write. Findings whose fix would change desired state (`deploy`, `upgrade --apply`, `backup setup`) show the command and a copy button.

The actions the app *does* take never rewrite what should run: backup now, the restore drill, apply host OS patches, rescan CVEs, reboot so those patches take effect, and remove a Base from this computer.

## Sessions

Every shell OwnBase opens on a Base is recorded — the whole session, input included — and this is where you watch them back.

**Replay** plays the recording at the speed it happened, with a scrubber. **Transcript** is the same thing as text, for searching and pasting.

Each session says who opened it. A shell you opened yourself shows *cli* or *app*; one an AI agent opened shows the agent's name. That distinction is the point of the feature: an agent with root on your machine is exactly as trustworthy as your ability to see what it did afterwards. The Base's logs tell you what the machine noticed. A recording tells you what was actually typed.

Recordings are [asciicast v2](https://docs.asciinema.org/manual/asciicast/v2/) files under `~/.ownbase/sessions/`, mode `0600`, and they are yours: `asciinema play <file>` replays one on a machine with no OwnBase installed at all. There is no way to turn recording off, because a switch would get used and an audit trail with holes in it answers no question.

## Vault

Shows where the vault file is, what it holds, when it was unlocked and when it will auto-lock; whether the credential agent is running and which socket it signs SSH on; and the keys it holds, one per Base, with the public half copyable.

**Lock now** discards the decrypted copy immediately. Otherwise the vault locks itself after a stretch of not being used, and anything that needs it will ask for your password again.

Changing the master password re-encrypts the file in place. It does not change any key inside it, so nothing on any Base needs to be re-authorized.

## When something goes wrong

**"OwnBase could not run its command-line tool."** The bundled `ownbasectl` did not start. That is a problem with the app install, not with any Base — yours are unaffected and still reachable from a terminal. Re-download the app.

**A Base shows as unreachable.** The app is telling you what an SSH connection did. `ownbasectl status <name>` from a terminal will give you the underlying error, and [troubleshooting.md](troubleshooting.md) covers the usual causes.

**The vault will not open.** Wrong password, or a file that is not a KDBX vault. If the file is on a synced folder, check that the sync finished — a half-downloaded placeholder is not a vault. See [troubleshooting.md](troubleshooting.md#wrong-master-password-or-the-vault-file-is-corrupt).

**Everything asks for your password again.** The vault auto-locked. Expected.

## See also

- [vault.md](vault.md) — the vault and the credential agent in full, including the CLI equivalents.
- [cli.md](cli.md) — every command the app is a front end for.
- [operating.md](operating.md) — the order of operations for changing what is deployed.
- [desktop/README.md](../desktop/README.md) — building and working on the app.
