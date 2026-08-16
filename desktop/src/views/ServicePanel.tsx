import { useCallback, useState } from "react";

import { CopyButton } from "../components/CopyButton";
import {
  Button,
  Field,
  Input,
  Panel,
  Spinner,
  Unavailable,
} from "../components/ui";
import type { Tone } from "../components/ui";
import * as api from "../lib/api";
import type { ServiceFields } from "../lib/api";
import { cx } from "../lib/cx";
import { shortRef } from "../lib/format";
import type { BaseStatus, ConfigPreview, ServiceStatus } from "../lib/types";
import { useAsync } from "../lib/useAsync";

import { DiffPreview } from "./DiffPreview";

/**
 * Services tab: live status, per-service secrets (reveal-on-click), and
 * add/update/remove with the mandatory dry-run → confirm flow.
 */
export function ServicePanel({
  base,
  status,
  onChanged,
}: {
  base: string;
  status: BaseStatus;
  onChanged: () => void;
}) {
  const services = status.services ?? [];
  const jobs = status.jobs ?? [];
  const [adding, setAdding] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);

  return (
    <div className="space-y-5">
      <Panel
        title="Services"
        subtitle="What ownbase.yaml asks for, and what the machine actually has running."
        action={
          <Button variant="secondary" onClick={() => setAdding((v) => !v)}>
            {adding ? "Cancel" : "Add service"}
          </Button>
        }
      >
        {adding && (
          <div className="mb-4 border-b border-zinc-800 pb-4">
            <ServiceEditForm
              base={base}
              mode="add"
              onDone={() => {
                setAdding(false);
                onChanged();
              }}
            />
          </div>
        )}
        {services.length === 0 && !adding ? (
          <Unavailable>
            Nothing is deployed yet. Add a service here, or declare one in{" "}
            <code className="font-mono text-xs">ownbase.yaml</code> in your config
            repo.
          </Unavailable>
        ) : (
          <ul className="divide-y divide-zinc-800">
            {services.map((service) => (
              <ServiceRow
                key={service.name}
                base={base}
                service={service}
                open={expanded === service.name}
                onToggle={() =>
                  setExpanded((cur) => (cur === service.name ? null : service.name))
                }
                onChanged={onChanged}
              />
            ))}
          </ul>
        )}
      </Panel>

      {jobs.length > 0 && (
        <Panel title="Scheduled jobs" subtitle="Timers, and how their last run went.">
          <ul className="divide-y divide-zinc-800">
            {jobs.map((job) => (
              <li key={job.name} className="py-3 first:pt-0 last:pb-0">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm text-zinc-200">{job.name}</span>
                  <span className="font-mono text-xs text-zinc-500">{job.schedule}</span>
                </div>
                <p className="mt-1 text-xs text-zinc-500">reuses {job.service}</p>
              </li>
            ))}
          </ul>
        </Panel>
      )}

      <ConfigYAMLPanel base={base} />
    </div>
  );
}

function ServiceRow({
  base,
  service,
  open,
  onToggle,
  onChanged,
}: {
  base: string;
  service: ServiceStatus;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const tone: Tone = !service.running ? "bad" : service.healthy ? "good" : "warn";
  const state = !service.running
    ? "stopped"
    : service.healthy
      ? "running"
      : "running, unhealthy";
  const domains = service.domains ?? (service.domain ? [service.domain] : []);
  const [mode, setMode] = useState<"secrets" | "edit" | "remove" | null>(null);

  return (
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <button
          type="button"
          onClick={onToggle}
          className="flex min-w-0 items-center gap-2 text-left text-sm text-zinc-200 hover:text-zinc-50"
        >
          <span
            className={cx(
              "inline-block h-1.5 w-1.5 rounded-full",
              tone === "good" && "bg-emerald-400",
              tone === "warn" && "bg-amber-400",
              tone === "bad" && "bg-red-400",
            )}
          />
          {service.name}
        </button>
        <span className="flex items-center gap-3 text-xs">
          {service.ref && (
            <span className="font-mono text-zinc-500" title={service.ref}>
              @{shortRef(service.ref)}
            </span>
          )}
          <span
            className={cx(
              tone === "good" && "text-emerald-300",
              tone === "warn" && "text-amber-300",
              tone === "bad" && "text-red-300",
            )}
          >
            {state}
          </span>
          <Button variant="ghost" onClick={onToggle}>
            {open ? "Less" : "Manage"}
          </Button>
        </span>
      </div>
      {(domains.length > 0 || service.repo) && (
        <div className="mt-1 space-y-0.5 pl-4 text-xs text-zinc-500">
          {domains.map((domain) => (
            <p key={domain} className="selectable font-mono">
              https://{domain}
            </p>
          ))}
          {service.repo && <p className="selectable font-mono">{service.repo}</p>}
        </div>
      )}
      {open && (
        <div className="mt-3 space-y-3 pl-4">
          <div className="flex flex-wrap gap-2">
            <Button
              variant={mode === "secrets" ? "primary" : "secondary"}
              onClick={() => setMode((m) => (m === "secrets" ? null : "secrets"))}
            >
              Secrets
            </Button>
            <Button
              variant={mode === "edit" ? "primary" : "secondary"}
              onClick={() => setMode((m) => (m === "edit" ? null : "edit"))}
            >
              Edit
            </Button>
            <Button
              variant="danger"
              onClick={() => setMode((m) => (m === "remove" ? null : "remove"))}
            >
              Remove
            </Button>
          </div>
          {mode === "secrets" && (
            <ServiceSecrets base={base} service={service.name} />
          )}
          {mode === "edit" && (
            <ServiceEditForm
              base={base}
              mode="update"
              service={service.name}
              defaults={{
                repo: service.repo,
                ref: service.ref,
                port: service.port,
                domain: service.domain,
                domains: service.domains,
              }}
              onDone={() => {
                setMode(null);
                onChanged();
              }}
            />
          )}
          {mode === "remove" && (
            <ServiceRemoveForm
              base={base}
              service={service.name}
              onDone={() => {
                setMode(null);
                onChanged();
              }}
            />
          )}
        </div>
      )}
    </li>
  );
}

function ServiceSecrets({ base, service }: { base: string; service: string }) {
  const load = useCallback(
    () => api.secretsListKeys(base, service),
    [base, service],
  );
  const state = useAsync(load);
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [newKey, setNewKey] = useState("");
  const [newVal, setNewVal] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

  const keys = state.data?.keys ?? [];

  async function reveal(key: string) {
    if (revealed[key] !== undefined) {
      setRevealed((r) => {
        const next = { ...r };
        delete next[key];
        return next;
      });
      return;
    }
    setBusyKey(key);
    setError(null);
    try {
      const r = await api.secretsGet(base, service, key);
      setRevealed((prev) => ({ ...prev, [key]: r.value }));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyKey(null);
    }
  }

  async function remove(key: string) {
    if (!window.confirm(`Delete secret ${key} from ${service}?`)) return;
    setBusyKey(key);
    setError(null);
    setNote(null);
    try {
      const r = await api.secretsDelete(base, service, key);
      if (r.escrow_note) setNote(r.escrow_note);
      setRevealed((prev) => {
        const next = { ...prev };
        delete next[key];
        return next;
      });
      state.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyKey(null);
    }
  }

  async function add(event: React.FormEvent) {
    event.preventDefault();
    if (!newKey.trim() || !newVal) return;
    setBusyKey("__add__");
    setError(null);
    setNote(null);
    try {
      const r = await api.secretsSet(base, service, { [newKey.trim()]: newVal });
      if (r.escrow_warning) setNote(r.escrow_warning);
      setNewKey("");
      setNewVal("");
      state.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyKey(null);
    }
  }

  return (
    <div className="space-y-3 rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
      <p className="text-xs leading-relaxed text-zinc-500">
        Age-encrypted on the Base. Values are never shown until you reveal one.
        RESTIC_PASSWORD on service backup must go through Backup → Rekey.
      </p>
      {state.loading ? (
        <Spinner />
      ) : keys.length === 0 ? (
        <p className="text-xs text-zinc-500">No secrets yet.</p>
      ) : (
        <ul className="divide-y divide-zinc-800">
          {keys.map((key) => (
            <li
              key={key}
              className="flex flex-wrap items-center justify-between gap-2 py-2 text-xs first:pt-0 last:pb-0"
            >
              <span className="font-mono text-zinc-300">{key}</span>
              <span className="flex items-center gap-2">
                {revealed[key] !== undefined && (
                  <>
                    <span className="selectable max-w-xs truncate font-mono text-zinc-400">
                      {revealed[key]}
                    </span>
                    <CopyButton value={revealed[key]} label="Copy" />
                  </>
                )}
                <Button
                  variant="ghost"
                  busy={busyKey === key}
                  onClick={() => void reveal(key)}
                >
                  {revealed[key] !== undefined ? "Hide" : "Reveal"}
                </Button>
                <Button
                  variant="danger"
                  busy={busyKey === key}
                  onClick={() => void remove(key)}
                >
                  Delete
                </Button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={add} className="flex flex-wrap items-end gap-2 border-t border-zinc-800 pt-3">
        <Field label="Key">
          <Input
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            placeholder="API_KEY"
            spellCheck={false}
            className="w-36"
          />
        </Field>
        <Field label="Value">
          <Input
            type="password"
            value={newVal}
            onChange={(e) => setNewVal(e.target.value)}
            placeholder="••••••••"
            className="w-48"
          />
        </Field>
        <Button
          type="submit"
          busy={busyKey === "__add__"}
          disabled={!newKey.trim() || !newVal}
        >
          Set
        </Button>
      </form>
      {error && <p className="text-xs text-red-300">{error}</p>}
      {note && <p className="text-xs text-amber-300">{note}</p>}
    </div>
  );
}

function ServiceEditForm({
  base,
  mode,
  service,
  defaults,
  onDone,
}: {
  base: string;
  mode: "add" | "update";
  service?: string;
  defaults?: Partial<ServiceFields>;
  onDone: () => void;
}) {
  const [name, setName] = useState(service ?? "");
  const [repo, setRepo] = useState(defaults?.repo ?? "");
  const [ref, setRef] = useState(defaults?.ref ?? "");
  const [port, setPort] = useState(defaults?.port ? String(defaults.port) : "");
  const [domain, setDomain] = useState(defaults?.domain ?? "");
  const [preview, setPreview] = useState<ConfigPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);

  function fields(): ServiceFields {
    const f: ServiceFields = {};
    if (repo.trim()) f.repo = repo.trim();
    if (ref.trim()) f.ref = ref.trim();
    if (port.trim()) f.port = Number(port);
    if (domain.trim()) f.domain = domain.trim();
    return f;
  }

  async function doPreview() {
    setBusy("preview");
    setError(null);
    setPreview(null);
    try {
      const f = fields();
      const p =
        mode === "add"
          ? await api.serviceAddPreview(base, name.trim(), f)
          : await api.serviceUpdatePreview(base, service!, f);
      setPreview(p);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function doApply() {
    if (!preview?.would_change) return;
    if (
      !window.confirm(
        `Commit and push this change to your config repo?\n\n${preview.commit_message}`,
      )
    ) {
      return;
    }
    setBusy("apply");
    setError(null);
    try {
      const f = fields();
      if (mode === "add") await api.serviceAdd(base, name.trim(), f);
      else await api.serviceUpdate(base, service!, f);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-3 rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
      {mode === "add" && (
        <Field label="Service name">
          <Input
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setPreview(null);
            }}
            placeholder="api"
            spellCheck={false}
          />
        </Field>
      )}
      <Field label="Repo" hint="External git URL (required to add).">
        <Input
          value={repo}
          onChange={(e) => {
            setRepo(e.target.value);
            setPreview(null);
          }}
          placeholder="git@github.com:org/app.git"
          spellCheck={false}
        />
      </Field>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label="Ref">
          <Input
            value={ref}
            onChange={(e) => {
              setRef(e.target.value);
              setPreview(null);
            }}
            placeholder="main"
            spellCheck={false}
          />
        </Field>
        <Field label="Port">
          <Input
            value={port}
            onChange={(e) => {
              setPort(e.target.value);
              setPreview(null);
            }}
            placeholder="8080"
            spellCheck={false}
          />
        </Field>
        <Field label="Domain">
          <Input
            value={domain}
            onChange={(e) => {
              setDomain(e.target.value);
              setPreview(null);
            }}
            placeholder="api.example.com"
            spellCheck={false}
          />
        </Field>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="secondary"
          busy={busy === "preview"}
          disabled={busy !== null || (mode === "add" && (!name.trim() || !repo.trim()))}
          onClick={() => void doPreview()}
        >
          Preview change
        </Button>
        {preview && (
          <Button
            busy={busy === "apply"}
            disabled={busy !== null || !preview.would_change}
            onClick={() => void doApply()}
          >
            {preview.would_change ? "Commit and apply" : "Already current"}
          </Button>
        )}
      </div>
      {preview && (
        <DiffPreview diff={preview.diff} commitMessage={preview.commit_message} />
      )}
      {error && <p className="text-xs text-red-300">{error}</p>}
    </div>
  );
}

function ServiceRemoveForm({
  base,
  service,
  onDone,
}: {
  base: string;
  service: string;
  onDone: () => void;
}) {
  const [confirm, setConfirm] = useState("");
  const [preview, setPreview] = useState<ConfigPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function doPreview() {
    setBusy("preview");
    setError(null);
    try {
      setPreview(await api.serviceRemovePreview(base, service));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function doApply() {
    if (confirm.trim() !== service || !preview?.would_change) return;
    if (
      !window.confirm(
        `Commit and push removal of ${service}?\n\n${preview.commit_message}`,
      )
    ) {
      return;
    }
    setBusy("apply");
    setError(null);
    try {
      await api.serviceRemove(base, service);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-3 rounded-md border border-red-500/20 bg-red-500/5 p-3">
      <p className="text-xs leading-relaxed text-zinc-400">
        Removes {service} from ownbase.yaml. Data volumes are not deleted by this
        alone — type the service name to confirm.
      </p>
      <Field label={`Type ${service} to confirm`}>
        <Input
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          spellCheck={false}
        />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="secondary"
          busy={busy === "preview"}
          disabled={busy !== null}
          onClick={() => void doPreview()}
        >
          Preview removal
        </Button>
        {preview && (
          <Button
            variant="danger"
            busy={busy === "apply"}
            disabled={
              busy !== null || confirm.trim() !== service || !preview.would_change
            }
            onClick={() => void doApply()}
          >
            Commit and remove
          </Button>
        )}
      </div>
      {preview && (
        <DiffPreview diff={preview.diff} commitMessage={preview.commit_message} />
      )}
      {error && <p className="text-xs text-red-300">{error}</p>}
    </div>
  );
}

function ConfigYAMLPanel({ base }: { base: string }) {
  const load = useCallback(() => api.configGetYAML(base), [base]);
  const state = useAsync(load);
  const [open, setOpen] = useState(false);

  return (
    <Panel
      title="ownbase.yaml"
      subtitle="Read-only view of what the Base is currently running from."
      action={
        <Button variant="secondary" onClick={() => setOpen((v) => !v)}>
          {open ? "Hide" : "Show"}
        </Button>
      }
    >
      {open &&
        (state.loading ? (
          <Spinner />
        ) : state.error ? (
          <p className="text-xs text-red-300">{state.error}</p>
        ) : (
          <pre className="selectable max-h-80 overflow-auto rounded-md border border-zinc-800 bg-zinc-950/60 p-3 font-mono text-[11px] leading-relaxed text-zinc-300">
            {state.data}
          </pre>
        ))}
    </Panel>
  );
}
