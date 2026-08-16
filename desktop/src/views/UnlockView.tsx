import { useState } from "react";
import { open } from "@tauri-apps/plugin-dialog";

import { Button, Card, ErrorNote, Field, Input } from "../components/ui";
import * as api from "../lib/api";
import type { Vault } from "../lib/useVault";

/**
 * The gate. Nothing else in the app can work until the vault is open, because
 * the vault is where every credential lives.
 *
 * Two situations share this screen because they look the same to the user —
 * "I opened OwnBase and it wants something from me" — but they ask for
 * different things: a location and a new password, or just the password.
 */
export function UnlockView({ vault }: { vault: Vault }) {
  const [mode, setMode] = useState<"default" | "recover">("default");

  return (
    <div className="flex h-full items-center justify-center bg-zinc-950 p-8">
      <div className="w-full max-w-md animate-fade-in">
        <Wordmark />
        {mode === "recover" ? (
          <RecoverVault
            onDone={() => void vault.refresh()}
            onCancel={() => setMode("default")}
          />
        ) : vault.phase === "absent" ? (
          <CreateVault vault={vault} onRecover={() => setMode("recover")} />
        ) : (
          <UnlockVault vault={vault} onRecover={() => setMode("recover")} />
        )}
      </div>
    </div>
  );
}

function Wordmark() {
  return (
    <div className="mb-8 text-center">
      <h1 className="text-2xl font-semibold tracking-tight text-zinc-100">OwnBase</h1>
      <p className="mt-1 text-sm text-zinc-500">Build faster with AI. Own everything.</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Unlocking an existing vault
// ---------------------------------------------------------------------------

function UnlockVault({
  vault,
  onRecover,
}: {
  vault: Vault;
  onRecover: () => void;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!password) return;
    setBusy(true);
    setError(null);
    try {
      await vault.unlock(password);
      setPassword("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Unlock your vault</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        Your master password decrypts the keys and tokens that reach your Bases. It
        is held in memory by the credential agent and never written anywhere.
      </p>

      <form onSubmit={submit} className="mt-5 space-y-4">
        <Field label="Master password">
          <Input
            type="password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="••••••••••••"
            autoComplete="current-password"
          />
        </Field>

        {error && <ErrorNote title="Could not unlock" detail={error} />}

        <Button type="submit" variant="primary" busy={busy} className="w-full">
          Unlock
        </Button>
      </form>

      {vault.status?.vault_path && (
        <p className="selectable mt-4 break-all font-mono text-xs text-zinc-600">
          {vault.status.vault_path}
        </p>
      )}
      <button
        type="button"
        onClick={onRecover}
        className="mt-4 text-xs text-zinc-500 underline-offset-2 hover:text-zinc-300 hover:underline"
      >
        Restore from recovery string…
      </button>
    </Card>
  );
}

function RecoverVault({
  onDone,
  onCancel,
}: {
  onDone: () => void;
  onCancel: () => void;
}) {
  const [recovery, setRecovery] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!recovery.trim() || !password) return;
    setBusy(true);
    setError(null);
    try {
      await api.vaultOpen(recovery.trim(), password);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setPassword("");
    }
  }

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        Restore from recovery string
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        Paste the recovery string printed when the vault was created (or from Vault
        → Recovery string), then your master password. This machine will point at
        the same remote vault and unlock it.
      </p>
      <form onSubmit={submit} className="mt-5 space-y-4">
        <Field label="Recovery string">
          <Input
            value={recovery}
            onChange={(e) => setRecovery(e.target.value)}
            placeholder="ownbase-recovery-v1:…"
            spellCheck={false}
            autoFocus
          />
        </Field>
        <Field label="Master password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </Field>
        {error && <ErrorNote title="Could not open vault" detail={error} />}
        <div className="flex gap-2">
          <Button type="button" variant="ghost" onClick={onCancel} className="flex-1">
            Cancel
          </Button>
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={!recovery.trim() || !password}
            className="flex-1"
          >
            Open vault
          </Button>
        </div>
      </form>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Creating (or adopting) a vault
// ---------------------------------------------------------------------------

function CreateVault({
  vault,
  onRecover,
}: {
  vault: Vault;
  onRecover: () => void;
}) {
  const [path, setPath] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const mismatch =
    confirmPassword.length > 0 && password !== confirmPassword
      ? "The two passwords do not match."
      : null;

  async function chooseFolder() {
    const chosen = await open({
      directory: true,
      multiple: false,
      title: "Where should your vault live?",
    });
    if (typeof chosen === "string") setPath(chosen);
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!path || !password || mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await vault.init(path, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setPassword("");
      setConfirmPassword("");
    }
  }

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Create your vault</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        One encrypted file holds the SSH keys and API tokens for every Base you
        own. You choose where it lives, and nothing but your master password can
        open it.
      </p>

      <form onSubmit={submit} className="mt-5 space-y-4">
        <Field
          label="Location"
          hint="Pick a folder your cloud storage syncs. The file is encrypted before it is written, so the provider only ever holds ciphertext — and a second copy is what saves you if this machine dies."
        >
          <div className="flex gap-2">
            <Input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="~/Dropbox/OwnBase"
              spellCheck={false}
            />
            <Button type="button" onClick={chooseFolder} className="shrink-0">
              Choose…
            </Button>
          </div>
        </Field>

        <Field
          label="Master password"
          hint="Long enough to be safe and memorable enough to type. There is no recovery: nothing in OwnBase and no one anywhere can open this file without it, which is the same property that makes it yours."
        >
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
        </Field>

        <Field label="Confirm password" error={mismatch}>
          <Input
            type="password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
          />
        </Field>

        {error && <ErrorNote title="Could not create the vault" detail={error} />}

        <Button
          type="submit"
          variant="primary"
          busy={busy}
          disabled={!path || !password || Boolean(mismatch)}
          className="w-full"
        >
          Create vault
        </Button>
      </form>

      <p className="mt-4 text-xs leading-relaxed text-zinc-600">
        Already have a vault on another machine? Point this at the same file and
        OwnBase will use it instead of creating a new one. For a remote vault,
        use the recovery string instead.
      </p>
      <button
        type="button"
        onClick={onRecover}
        className="mt-3 text-xs text-zinc-500 underline-offset-2 hover:text-zinc-300 hover:underline"
      >
        Restore from recovery string…
      </button>
    </Card>
  );
}
