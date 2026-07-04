#!/usr/bin/env bash
# OwnBase installer — minimal native bootstrap.
# https://ownbase.ai/install
#
# Usage:
#   sudo bash install.sh
#   OWNBASE_VERSION=v0.1.0 sudo -E bash install.sh   # pin a daemon release
#   OWNBASE_SKIP_VERIFY=1 bash install.sh            # dev/test only — never production
#
# This script is normally not run by hand: `ownbasectl create` uploads and
# runs it for you (locally on a Multipass VM or over SSH on a remote
# server). All configuration is via environment variables — see below.
#
# This script does only the absolute minimum natively:
#   1. Verify the signed daemon binary (minisign).
#   2. Create the ownbase system user.
#   3. Drop the binary to /opt/ownbase/bin/.
#   4. Install the systemd service unit.
#   5. Enable and start the service.
#
# Everything else — Podman install, host hardening, bare repo setup,
# service reconciliation — is handled by the daemon's reconcile pass zero
# (internal/install/). Resumable: re-running after failure just continues.
#
# Signed binary decision (M5):
#   Signatures are created with minisign (https://jedisct1.github.io/minisign/).
#   The public key below is the OwnBase release key. Users who fork OwnBase
#   substitute their own public key and re-sign the binary.
#
#   To verify manually:
#     minisign -Vm ownbased-linux-amd64 -P 'RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3'

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration (overridable by environment)
# ---------------------------------------------------------------------------

OWNBASE_VERSION="${OWNBASE_VERSION:-latest}"
OWNBASE_USER="${OWNBASE_USER:-ownbase}"
OWNBASE_GROUP="${OWNBASE_GROUP:-ownbase}"
OWNBASE_BASE_DIR="${OWNBASE_BASE_DIR:-/opt/ownbase}"
OWNBASE_DAEMON_DIR="${OWNBASE_BASE_DIR}/bin"
OWNBASE_DAEMON_BIN="${OWNBASE_DAEMON_DIR}/ownbased"
OWNBASE_SKIP_VERIFY="${OWNBASE_SKIP_VERIFY:-0}"

# OWNBASE_LOCAL_BINARY: path to an already-built ownbased binary on this
# machine. When set, the download+minisign-verify step is skipped entirely
# and this file is installed instead. Used by `ownbasectl create` (the
# local-VM path, no --remote), which cross-compiles the daemon and transfers
# it into the VM directly — there is no need for an HTTP release server on
# the local-VM path.
OWNBASE_LOCAL_BINARY="${OWNBASE_LOCAL_BINARY:-}"

# OWNBASE_REBUILD / OWNBASE_BACKUP_REPO / OWNBASE_FORCE_REBUILD: reconstruction
# mode. When OWNBASE_REBUILD=1, the daemon's --rebuild path runs once (restoring
# the latest snapshot from OWNBASE_BACKUP_REPO into place) before the normal
# service starts. Restic credentials (RESTIC_PASSWORD, AWS_*, etc.) must
# already be present in this script's environment — they are inherited by the
# rebuild subprocess and never written to disk by this script. Used by
# `ownbasectl restore`.
OWNBASE_REBUILD="${OWNBASE_REBUILD:-0}"
OWNBASE_BACKUP_REPO="${OWNBASE_BACKUP_REPO:-}"
OWNBASE_FORCE_REBUILD="${OWNBASE_FORCE_REBUILD:-0}"

# Owner credentials — passed to the daemon for first-run Forgejo setup.
# OWNBASE_OWNER_PASSWORD: Forgejo web-UI login password. If not set, a
#   random one is generated and printed at the end of this script.
# OWNBASE_OWNER_SSH_KEY: SSH public key to register with Forgejo so the
#   owner can git push/pull over SSH without a password.
#   Example: "ssh-ed25519 AAAA... you@laptop"
OWNBASE_OWNER_PASSWORD="${OWNBASE_OWNER_PASSWORD:-}"
OWNBASE_OWNER_SSH_KEY="${OWNBASE_OWNER_SSH_KEY:-}"

# OWNBASE_DRIVEN_BY_CTL: set to 1 by `ownbasectl create`, which registers
# the profile automatically after the install — the completion footer then
# skips the manual `ownbasectl adopt` instructions.
OWNBASE_DRIVEN_BY_CTL="${OWNBASE_DRIVEN_BY_CTL:-0}"

# OWNBASE_SSH_PORT: the SSH port hardened into UFW and the daemon's exposure
# allowlist. If your server's sshd listens on a non-standard port, set this
# before running the installer so the daemon does not flag that port as an
# unexpected internet-reachable listener.
OWNBASE_SSH_PORT="${OWNBASE_SSH_PORT:-22}"

# Domain configuration — seeded into ownbase.yaml on first bootstrap so
# Caddy configures TLS on the first reconcile without a manual edit/push.
# FORGEJO_DOMAIN: public hostname for the Forgejo git UI (e.g. git.mysite.com).
#   Point your DNS at this server before running the installer.
# CADDY_EMAIL: ACME/Let's Encrypt contact email for automatic TLS.
FORGEJO_DOMAIN="${FORGEJO_DOMAIN:-}"
CADDY_EMAIL="${CADDY_EMAIL:-}"

# OwnBase release signing public key (minisign).
# This is the development key — replace with the production key before release.
# Generated with: minisign -G -p ownbase-release.pub -s ownbase-release.key
MINISIGN_PUBLIC_KEY="RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"

RELEASE_BASE_URL="${RELEASE_BASE_URL:-https://releases.ownbase.ai/daemon}"

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

info()  { echo "[ownbase] $*" >&2; }
error() { echo "[ownbase] ERROR: $*" >&2; }
die()   { error "$*"; exit 1; }

require_root() {
    if [[ "$(id -u)" -ne 0 ]]; then
        die "This installer must run as root (sudo bash install.sh)"
    fi
}

detect_arch() {
    local arch
    arch="$(uname -m)"
    case "$arch" in
        x86_64)  echo "amd64" ;;
        aarch64) echo "arm64" ;;
        *)       die "Unsupported architecture: $arch" ;;
    esac
}

detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        echo "$ID"
    else
        die "Cannot detect OS"
    fi
}

detect_version_id() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        echo "$VERSION_ID"
    fi
}

check_ubuntu() {
    local os version
    os="$(detect_os)"
    version="$(detect_version_id)"
    [[ "$os" == "ubuntu" ]] || die "OwnBase requires Ubuntu (detected: $os)"
    info "Ubuntu $version detected"
}

# ---------------------------------------------------------------------------
# Step 1: Download daemon binary
# ---------------------------------------------------------------------------

download_agent() {
    if [[ -n "$OWNBASE_LOCAL_BINARY" ]]; then
        [[ -f "$OWNBASE_LOCAL_BINARY" ]] || die "OWNBASE_LOCAL_BINARY=$OWNBASE_LOCAL_BINARY not found"
        info "Using local daemon binary: $OWNBASE_LOCAL_BINARY (signature verification skipped)"
        info "NEVER use OWNBASE_LOCAL_BINARY for a production install over the internet"
        _DAEMON_TMPDIR="$(mktemp -d)"
        cp "$OWNBASE_LOCAL_BINARY" "${_DAEMON_TMPDIR}/ownbased"
        chmod +x "${_DAEMON_TMPDIR}/ownbased"
        return
    fi

    local arch version url sig_url
    arch="$(detect_arch)"
    version="$OWNBASE_VERSION"

    if [[ "$version" == "latest" ]]; then
        url="${RELEASE_BASE_URL}/latest/ownbased-linux-${arch}"
    else
        url="${RELEASE_BASE_URL}/${version}/ownbased-linux-${arch}"
    fi
    sig_url="${url}.minisig"

    # Use a global tmpdir so the EXIT trap does not delete it before install_binary runs.
    _DAEMON_TMPDIR="$(mktemp -d)"

    info "Downloading daemon binary from $url ..."
    if ! curl -fsSL -o "${_DAEMON_TMPDIR}/ownbased" "$url"; then
        die "Failed to download daemon binary from $url"
    fi

    if [[ "$OWNBASE_SKIP_VERIFY" == "1" ]]; then
        info "WARNING: signature verification skipped (OWNBASE_SKIP_VERIFY=1)"
        info "NEVER skip verification in production"
    else
        verify_signature "${_DAEMON_TMPDIR}/ownbased" "$sig_url" "${_DAEMON_TMPDIR}"
    fi

    chmod +x "${_DAEMON_TMPDIR}/ownbased"
}

# ---------------------------------------------------------------------------
# Step 2: Verify signature
# ---------------------------------------------------------------------------

verify_signature() {
    local binary="$1" sig_url="$2" tmpdir="$3"

    info "Verifying daemon binary signature ..."

    # Install minisign if not present.
    if ! command -v minisign &>/dev/null; then
        info "Installing minisign for signature verification ..."
        apt-get install -y -q minisign || die "Cannot install minisign"
    fi

    # Download signature file.
    if ! curl -fsSL -o "${tmpdir}/ownbased.minisig" "$sig_url"; then
        die "Failed to download signature from $sig_url"
    fi

    # Verify.
    if ! minisign -Vm "$binary" \
        -x "${tmpdir}/ownbased.minisig" \
        -P "$MINISIGN_PUBLIC_KEY"; then
        die "Signature verification FAILED — binary may be tampered. Aborting."
    fi
    info "Signature verified OK"
}

# ---------------------------------------------------------------------------
# Step 3: Create system user
# ---------------------------------------------------------------------------

create_ownbase_user() {
    if id "$OWNBASE_USER" &>/dev/null; then
        info "User $OWNBASE_USER already exists"
        return
    fi
    info "Creating system user $OWNBASE_USER ..."
    useradd \
        --system \
        --shell /bin/bash \
        --home-dir "${OWNBASE_BASE_DIR}" \
        --create-home \
        --user-group \
        "$OWNBASE_USER"
    info "User $OWNBASE_USER created"
}

# ---------------------------------------------------------------------------
# Step 4: Install binary
# ---------------------------------------------------------------------------

install_binary() {
    mkdir -p "$OWNBASE_DAEMON_DIR"
    cp "${_DAEMON_TMPDIR}/ownbased" "$OWNBASE_DAEMON_BIN"
    chown "${OWNBASE_USER}:${OWNBASE_GROUP}" "$OWNBASE_DAEMON_BIN"
    chmod 0755 "$OWNBASE_DAEMON_BIN"
    rm -rf "${_DAEMON_TMPDIR}"
    info "Daemon binary installed to $OWNBASE_DAEMON_BIN"
}

# ---------------------------------------------------------------------------
# Step 4.5: Rebuild mode — restore latest backup before the service starts
# ---------------------------------------------------------------------------

# run_rebuild_if_requested restores the latest verified backup snapshot into
# place using the daemon's own --rebuild path, before the systemd service is
# installed and started. This is the reconstruction path (see
# internal/backup/rebuild.go) made reachable from a fresh-machine install:
#
#   current = restore(backups)
#   running = reconcile(compile(repo, secrets), current)
#
# A no-op unless OWNBASE_REBUILD=1. Fails the whole install on error rather
# than silently continuing with an empty Base — a restore that silently
# didn't happen is worse than an install that stops and says so.
run_rebuild_if_requested() {
    [[ "$OWNBASE_REBUILD" == "1" ]] || return 0
    [[ -n "$OWNBASE_BACKUP_REPO" ]] || die "OWNBASE_REBUILD=1 requires OWNBASE_BACKUP_REPO to be set"

    # `ownbased --rebuild` shells out to restic directly and exits before
    # ever reaching pass zero (pass zero, which installs restic, only runs
    # on the *next* normal daemon start — see cmd/ownbased/main.go). Install
    # it here so restore on a bare machine does not fail for lack of it.
    if ! command -v restic &>/dev/null; then
        info "Installing restic for backup restore ..."
        apt-get install -y -q restic || die "Cannot install restic"
    fi

    info "Rebuild mode: restoring latest backup from ${OWNBASE_BACKUP_REPO} ..."
    local force_flag=()
    [[ "$OWNBASE_FORCE_REBUILD" == "1" ]] && force_flag=(--force-rebuild)

    if ! "$OWNBASE_DAEMON_BIN" --rebuild --backup-repo "$OWNBASE_BACKUP_REPO" "${force_flag[@]}"; then
        die "Rebuild failed — aborting install. Verify the backup repo URL and restic credentials (RESTIC_PASSWORD, etc.) and re-run."
    fi
    info "Rebuild complete — data restored. Continuing with normal install ..."
}

# ---------------------------------------------------------------------------
# Step 5.5: Write first-run credentials file
# ---------------------------------------------------------------------------

# write_first_run_env saves owner credentials to a one-time file that the
# daemon reads on first bootstrap, then deletes. The file is readable only
# by root (0600) and lives in the same directory as the token.
write_first_run_env() {
    local first_run_file="${OWNBASE_BASE_DIR}/first-run.env"
    local password="$1"
    local ssh_key="$2"
    local forgejo_domain="$3"
    local caddy_email="$4"

    # Nothing to write.
    if [[ -z "$password" && -z "$ssh_key" && -z "$forgejo_domain" && -z "$caddy_email" ]]; then
        return
    fi

    install -o root -g root -m 0600 /dev/null "$first_run_file"
    [[ -n "$password"        ]] && printf 'OWNER_PASSWORD=%s\n'  "$password"        >> "$first_run_file"
    [[ -n "$ssh_key"         ]] && printf 'OWNER_SSH_KEY=%s\n'   "$ssh_key"         >> "$first_run_file"
    [[ -n "$forgejo_domain"  ]] && printf 'FORGEJO_DOMAIN=%s\n'  "$forgejo_domain"  >> "$first_run_file"
    [[ -n "$caddy_email"     ]] && printf 'CADDY_EMAIL=%s\n'     "$caddy_email"     >> "$first_run_file"
    info "Owner credentials saved for daemon first-run setup."
}

# generate_password produces a random alphanumeric password.
generate_password() {
    tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 20 || true
}

# generate_token produces a random 32-char alphanumeric token.
generate_token() {
    tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32 || true
}

# write_api_token writes the API Bearer token to /opt/ownbase/api-token.
# Called before install_systemd_service so the daemon picks it up on first start.
write_api_token() {
    local token="$1"
    local token_file="${OWNBASE_BASE_DIR}/api-token"
    install -o root -g root -m 0600 /dev/null "$token_file"
    printf '%s' "$token" > "$token_file"
    info "API token written to $token_file"
}

# write_forgejo_admin_pass persists the Forgejo admin password so the daemon
# can serve it via the credentials API after first-run.env is deleted.
write_forgejo_admin_pass() {
    local password="$1"
    local pass_file="${OWNBASE_BASE_DIR}/forgejo-admin-pass"
    install -o root -g root -m 0600 /dev/null "$pass_file"
    printf '%s' "$password" > "$pass_file"
}

install_systemd_service() {
    local service_file="/etc/systemd/system/ownbased.service"
    cat > "$service_file" <<EOF
[Unit]
Description=OwnBase Daemon
Documentation=https://ownbase.ai/docs
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# Pass zero (host hardening) requires root to install packages, configure UFW,
# and write to /etc/. After pass zero the daemon only talks to podman/systemd.
User=root
ExecStart=${OWNBASE_DAEMON_BIN} --ssh-port ${OWNBASE_SSH_PORT}
Restart=always
RestartSec=5
# Journal logging
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$service_file"
    systemctl daemon-reload
    info "Systemd service installed: ownbased.service"
}

# ---------------------------------------------------------------------------
# Step 6: Enable and start service
# ---------------------------------------------------------------------------

enable_service() {
    # Pre-create directories that ReadWritePaths requires before the daemon can
    # run pass zero (pass zero installs fail2ban which creates these).
    mkdir -p /etc/fail2ban/jail.d

    systemctl enable ownbased
    systemctl start ownbased
    info "ownbased service enabled and started"
    info ""
    info "The daemon is now running. Follow its progress with:"
    info "  journalctl -u ownbased -f"
    info ""
    info "The daemon will harden this host, set up the container runtime,"
    info "and bootstrap your Base. This takes 1–3 minutes on a fresh machine."
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
    require_root
    check_ubuntu

    info "OwnBase Installer — version: ${OWNBASE_VERSION}"
    info "This script does only the minimum natively."
    info "All hardening and service setup is handled by the daemon."
    info ""

    # Generate a Forgejo owner password if the caller did not supply one.
    local owner_password="$OWNBASE_OWNER_PASSWORD"
    local generated_password=0
    if [[ -z "$owner_password" ]]; then
        owner_password="$(generate_password)"
        generated_password=1
    fi

    local api_token
    api_token="$(generate_token)"

    download_agent
    create_ownbase_user
    install_binary
    run_rebuild_if_requested
    write_first_run_env "$owner_password" "$OWNBASE_OWNER_SSH_KEY" "$FORGEJO_DOMAIN" "$CADDY_EMAIL"
    write_api_token "$api_token"
    write_forgejo_admin_pass "$owner_password"
    install_systemd_service
    enable_service

    # If the owner provided an SSH public key, also add it to the invoking
    # user's authorized_keys so that ownbasectl can open an SSH tunnel for
    # its remote subcommands (status, secrets, forgejo, etc.).
    if [[ -n "$OWNBASE_OWNER_SSH_KEY" ]]; then
        local invoker_home
        invoker_home="$(getent passwd "${SUDO_USER:-ubuntu}" | cut -d: -f6)"
        local auth_keys="${invoker_home}/.ssh/authorized_keys"
        mkdir -p "${invoker_home}/.ssh"
        chmod 700 "${invoker_home}/.ssh"
        if ! grep -qF "$OWNBASE_OWNER_SSH_KEY" "$auth_keys" 2>/dev/null; then
            printf '%s\n' "$OWNBASE_OWNER_SSH_KEY" >> "$auth_keys"
            chmod 600 "$auth_keys"
            chown -R "${SUDO_USER:-ubuntu}:${SUDO_USER:-ubuntu}" "${invoker_home}/.ssh"
            info "Owner SSH key added to ${auth_keys} (for ownbasectl)"
        fi
    fi

    info "Installation complete."

    info ""
    info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    if [[ "$generated_password" -eq 1 ]]; then
        info "  Forgejo owner credentials"
        info "    Username : ownbase"
        info "    Password : ${owner_password}"
        if [[ -n "$FORGEJO_DOMAIN" ]]; then
        info "    URL      : https://${FORGEJO_DOMAIN}"
        fi
        info ""
    fi
    info "  OwnBase API token (for ownbasectl)"
    info "    Token    : ${api_token}"
    info ""
    if [[ "$OWNBASE_DRIVEN_BY_CTL" == "1" ]]; then
        info "  This install is driven by ownbasectl — the profile is registered"
        info "  on your machine automatically."
    else
        info "  Register this Base on your laptop:"
        info "    ownbasectl adopt mybase \\"
        info "      --host <server-ip-or-hostname> \\"
        info "      --token ${api_token}"
    fi
    info ""
    info "  Save these credentials — they will not be shown again."
    info "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

main "$@"
