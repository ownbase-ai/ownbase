import { useState } from "react";

import { CopyButton } from "../components/CopyButton";
import {
  Badge,
  Button,
  CommandLine,
  Dot,
  ErrorNote,
  Field,
  Input,
  Panel,
  Row,
} from "../components/ui";
import * as api from "../lib/api";
import { absolute, ago, span, until } from "../lib/format";
import type { BaseSummary } from "../lib/types";
import type { Vault } from "../lib/useVault";

/**
 * What the vault holds and how it behaves.
 *
 * The honest version of a credential screen: it says where the file is, what is
 * in it, when it will lock, and how to open it without OwnBase. It never shows a
 * secret, because it never has one — the private keys live in the agent's memory
 * and the app only ever asks the CLI to use them.
 */
export function VaultView({ vault, bases }: { vault: Vault; bases: BaseSummary[] }) {
  const status = vault.status;
  const withKeys = bases.filter((b) => b.has_key);

  return (
    <div className="mx-auto w-full max-w-3xl space-y-5 overflow-y-auto px-8 py-8">
      <header>
        <h1 className="text-lg font-medium text-zinc-100">Vault</h1>
        <p className="mt-1 text-sm leading-relaxed text-zinc-500">
          One encrypted file holds every credential that reaches your Bases. You chose
          where it lives; nothing but your master password opens it.
        </p>
      </header>

      <Panel
        title="Status"
        action={
          <Button variant="secondary" onClick={() => void vault.lock()}>
            Lock now
          </Button>
        }
      >
        <Row label="State">
          <Badge tone={status?.unlocked ? "good" : "warn"}>
            <Dot tone={status?.unlocked ? "good" : "warn"} />
            {status?.unlocked ? "unlocked" : "locked"}
          </Badge>
        </Row>
        <Row label="File" title={status?.vault_path}>
          <span className="font-mono text-xs">{status?.vault_path ?? "—"}</span>
        </Row>
        <Row label="Holds">
          {status?.bases ?? 0} Base{(status?.bases ?? 0) === 1 ? "" : "s"},{" "}
          {status?.keys ?? 0} owner key{(status?.keys ?? 0) === 1 ? "" : "s"}
        </Row>
        <Row label="Unlocked" title={absolute(status?.unlocked_at)}>
          {ago(status?.unlocked_at, "—")}
        </Row>
        <Row label="Auto-locks" title={absolute(status?.locks_at)}>
          {status?.idle_timeout_seconds
            ? `${until(status.locks_at, "—")} unless something uses it`
            : "never — no idle timeout set"}
        </Row>
        {status?.idle_timeout_seconds ? (
          <Row label="Idle timeout">{span(status.idle_timeout_seconds)}</Row>
        ) : null}
        {status?.vault_path && (
          <div className="mt-3 flex justify-end">
            <CopyButton value={status.vault_path} label="Copy path" />
          </div>
        )}
      </Panel>

      <Panel
        title="The credential agent"
        subtitle="A small resident process, like ssh-agent, holding the unlocked vault in memory."
      >
        <Row label="Running">
          {status?.running ? (
            <span className="text-emerald-300">yes (pid {status.pid})</span>
          ) : (
            <span className="text-zinc-500">no</span>
          )}
        </Row>
        {status?.ssh_agent_socket && (
          <Row label="Signs SSH on" title={status.ssh_agent_socket}>
            <span className="font-mono text-xs">{status.ssh_agent_socket}</span>
          </Row>
        )}
        {status?.version && <Row label="Version">{status.version}</Row>}
        <p className="mt-3 text-xs leading-relaxed text-zinc-500">
          Private keys never leave the agent and are never written to disk. When
          something needs to authenticate, it asks the agent for a signature — which
          is also why a coding agent can reach your Bases without ever being handed a
          key.
        </p>
      </Panel>

      <Panel
        title="Owner keys"
        subtitle="One per Base, so retiring a Base revokes exactly one credential."
      >
        {withKeys.length === 0 ? (
          <p className="text-sm text-zinc-500">No keys yet.</p>
        ) : (
          <ul className="divide-y divide-zinc-800">
            {withKeys.map((base) => (
              <li
                key={base.name}
                className="flex items-center justify-between gap-3 py-2.5 text-sm first:pt-0 last:pb-0"
              >
                <span className="text-zinc-200">{base.name}</span>
                <span className="text-xs text-zinc-500">
                  {base.host ? base.host : "no machine yet"}
                  {base.has_token ? " · API token stored" : ""}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <ChangePassword />

      <Panel
        title="If OwnBase disappeared tomorrow"
        subtitle="The escape hatch is not a promise, it is the file format."
      >
        <p className="text-sm leading-relaxed text-zinc-400">
          The vault is a standard KDBX 4 database — the KeePass format. KeePassXC and
          every other KeePass client can open it with your master password, read the
          hosts and tokens, and export the private keys. Nothing here depends on this
          app, or on us.
        </p>
        <p className="mt-3 text-xs leading-relaxed text-zinc-500">
          Back the file up. It holds the only copy of the keys that reach your Bases,
          and there is no recovery: lose both it and your master password and no one
          can get in, which is the same property that makes it safe to keep in cloud
          storage.
        </p>
      </Panel>
    </div>
  );
}

function ChangePassword() {
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const mismatch =
    confirm.length > 0 && password !== confirm ? "The two passwords do not match." : null;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!password || mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await api.vaultChangePassword(password);
      setDone(true);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      setPassword("");
      setConfirm("");
    }
  }

  return (
    <Panel
      title="Master password"
      subtitle="Re-encrypts the vault. The old password is not needed — it is already unlocked."
      action={
        !open && (
          <Button variant="secondary" onClick={() => setOpen(true)}>
            Change
          </Button>
        )
      }
    >
      {done && !open && (
        <p className="text-sm text-emerald-300">
          Changed. Every machine that syncs this file will need the new password.
        </p>
      )}
      {!open && !done && (
        <p className="text-sm leading-relaxed text-zinc-500">
          You can also do this from a terminal with{" "}
          <CommandLine>ownbasectl vault passwd</CommandLine>.
        </p>
      )}
      {open && (
        <form onSubmit={submit} className="space-y-4">
          <Field label="New master password">
            <Input
              type="password"
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          <Field label="Confirm" error={mismatch}>
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          {error && <ErrorNote title="Could not change the password" detail={error} />}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="primary"
              busy={busy}
              disabled={!password || Boolean(mismatch)}
            >
              Change password
            </Button>
          </div>
        </form>
      )}
    </Panel>
  );
}
