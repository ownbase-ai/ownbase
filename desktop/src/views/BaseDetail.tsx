import { useCallback, useState } from "react";

import {
  Badge,
  Button,
  CommandLine,
  Dot,
  EmptyState,
  ErrorNote,
  Field,
  Input,
  LogView,
  Panel,
  Row,
  Spinner,
  Tabs,
  Unavailable,
} from "../components/ui";
import type { Tone } from "../components/ui";
import * as api from "../lib/api";
import type { StreamEvent } from "../lib/cli";
import { cx } from "../lib/cx";
import { absolute, ago, shortRef, until } from "../lib/format";
import type {
  BaseStatus,
  BaseSummary,
  Checkup,
  Finding,
  ServiceStatus,
  VulnSummary,
} from "../lib/types";
import { useAsync } from "../lib/useAsync";

type Tab = "overview" | "services" | "security" | "backups" | "updates" | "activity";

/**
 * Everything known about one Base.
 *
 * Desired state is read-only here. Config changes are commits to a Git repo
 * the user owns, and giving the window a second way to make them would mean
 * two answers to "what is deployed". Actions the window does take never
 * rewrite what should run: backup now, the restore drill, apply host OS
 * patches, rescan CVEs, reboot so those patches take effect, and forget this
 * Base on this computer.
 */
export function BaseDetail({
  base,
  onRemoved,
}: {
  base: BaseSummary;
  onRemoved: () => void;
}) {
  const [tab, setTab] = useState<Tab>("overview");
  const load = useCallback(() => api.checkup(base.name), [base.name]);
  const state = useAsync<Checkup>(load);

  if (!base.registered || !base.host) {
    return <NotReachableYet base={base} onRemoved={onRemoved} />;
  }

  const findings = state.data?.findings ?? [];
  const status = state.data?.status;

  return (
    <div className="flex h-full flex-col">
      <Header base={base} findings={findings} state={state} />

      <div className="border-b border-zinc-800 px-8">
        <Tabs<Tab>
          active={tab}
          onChange={setTab}
          tabs={[
            {
              id: "overview",
              label: "Overview",
              badge: findings.length,
              badgeTone: "warn",
            },
            { id: "services", label: "Services" },
            { id: "security", label: "Security" },
            { id: "backups", label: "Backups" },
            { id: "updates", label: "Updates" },
            { id: "activity", label: "Activity" },
          ]}
        />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 py-6">
        {state.loading ? (
          <div className="flex items-center gap-3 text-sm text-zinc-500">
            <Spinner /> Asking {base.name} how it is doing…
          </div>
        ) : state.error || !status ? (
          <div className="space-y-8">
            <UnreachableNote base={base} detail={state.error} onRetry={state.reload} />
            <RemoveBase base={base} onRemoved={onRemoved} />
          </div>
        ) : (
          <Body
            tab={tab}
            base={base}
            status={status}
            findings={findings}
            onChanged={state.reload}
            onOpenTab={setTab}
            onRemoved={onRemoved}
          />
        )}
      </div>
    </div>
  );
}

function Body({
  tab,
  base,
  status,
  findings,
  onChanged,
  onOpenTab,
  onRemoved,
}: {
  tab: Tab;
  base: BaseSummary;
  status: BaseStatus;
  findings: Finding[];
  onChanged: () => void;
  onOpenTab: (tab: Tab) => void;
  onRemoved: () => void;
}) {
  switch (tab) {
    case "overview":
      return (
        <Overview
          base={base}
          status={status}
          findings={findings}
          onChanged={onChanged}
          onOpenTab={onOpenTab}
          onRemoved={onRemoved}
        />
      );
    case "services":
      return <Services status={status} />;
    case "security":
      return <Security base={base} status={status} onChanged={onChanged} />;
    case "backups":
      return <Backups base={base} status={status} onChanged={onChanged} />;
    case "updates":
      return <Updates base={base} status={status} onChanged={onChanged} />;
    case "activity":
      return <Activity status={status} />;
  }
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

function Header({
  base,
  findings,
  state,
}: {
  base: BaseSummary;
  findings: Finding[];
  state: { loading: boolean; refreshing: boolean; error: string | null; reload: () => void };
}) {
  const verdict: { tone: Tone; text: string } = state.error
    ? { tone: "unknown", text: "Not answering" }
    : state.loading
      ? { tone: "unknown", text: "Checking" }
      : findings.length === 0
        ? { tone: "good", text: "All clear" }
        : {
            tone: "warn",
            text: `${findings.length} thing${findings.length === 1 ? "" : "s"} to look at`,
          };

  return (
    <header className="flex items-start justify-between gap-6 px-8 pb-4 pt-8">
      <div className="min-w-0">
        <div className="flex items-center gap-3">
          <h1 className="truncate text-lg font-medium text-zinc-100">{base.name}</h1>
          <Badge tone={verdict.tone}>
            <Dot tone={verdict.tone} />
            {verdict.text}
          </Badge>
        </div>
        <p className="selectable mt-1 truncate font-mono text-xs text-zinc-500">
          {base.ssh_user}@{base.host}
          {base.ssh_port && base.ssh_port !== 22 ? `:${base.ssh_port}` : ""}
        </p>
      </div>
      <Button variant="secondary" busy={state.refreshing} onClick={state.reload}>
        Refresh
      </Button>
    </header>
  );
}

/** A Base that exists in the vault as a key, with no machine behind it yet. */
function NotReachableYet({
  base,
  onRemoved,
}: {
  base: BaseSummary;
  onRemoved: () => void;
}) {
  const unregistered = base.kind === "unregistered-vm";
  return (
    <div className="flex h-full flex-col px-8 py-8">
      <h1 className="text-lg font-medium text-zinc-100">{base.name}</h1>
      <div className="mt-6 space-y-8">
        {unregistered ? (
          <EmptyState title="A local VM with this name, but no Base">
            <p>
              Multipass has a VM called <strong>{base.name}</strong> that OwnBase does
              not know about. Adopt it with{" "}
              <CommandLine>ownbasectl adopt {base.name}</CommandLine>, or destroy it
              below.
            </p>
          </EmptyState>
        ) : (
          <EmptyState title="This Base has a key, but no machine yet">
            <p>
              Your vault holds an owner key for <strong>{base.name}</strong> and
              nothing has claimed it. Create a server with that key authorized, then
              run{" "}
              <CommandLine>
                ownbasectl create {base.name} --remote root@&lt;ip&gt; --wait
              </CommandLine>
              . For a local Multipass VM under this same name, remove this Base first
              (below) so the key is free, then use <em>Set up a Base</em>.
            </p>
          </EmptyState>
        )}
        <RemoveBase base={base} onRemoved={onRemoved} />
      </div>
    </div>
  );
}

function UnreachableNote({
  base,
  detail,
  onRetry,
}: {
  base: BaseSummary;
  detail: string | null;
  onRetry: () => void;
}) {
  return (
    <div className="space-y-4">
      <ErrorNote
        title={`${base.name} did not answer`}
        detail={detail}
        onRetry={onRetry}
      />
      <p className="text-sm leading-relaxed text-zinc-500">
        The app reaches a Base exactly the way the CLI does — an SSH tunnel to the
        daemon's loopback API — so this is the machine or the network, not the app.
        If it was just created it may still be hardening. To look at the machine
        itself, open a recorded shell with{" "}
        <CommandLine>ownbasectl ssh {base.name}</CommandLine>.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------

function Overview({
  base,
  status,
  findings,
  onChanged,
  onOpenTab,
  onRemoved,
}: {
  base: BaseSummary;
  status: BaseStatus;
  findings: Finding[];
  onChanged: () => void;
  onOpenTab: (tab: Tab) => void;
  onRemoved: () => void;
}) {
  const services = status.services ?? [];
  const running = services.filter((s) => s.running).length;
  const unhealthy = services.filter((s) => s.running && !s.healthy).length;

  return (
    <div className="space-y-5">
      <Panel
        title={findings.length === 0 ? "Nothing needs attention" : "Worth a look"}
        subtitle={
          findings.length === 0
            ? "The last checkup found no problems."
            : "Only things you can finish. Unfixed CVEs and other readings live on their tabs."
        }
      >
        {findings.length === 0 ? (
          <Unavailable>
            Backups are proven restorable, the firewall is up, no unexpected port is
            reachable, and nothing has drifted from what your config repo says.
          </Unavailable>
        ) : (
          <ul className="space-y-3">
            {findings.map((finding) => (
              <FindingRow
                key={finding.summary}
                base={base.name}
                finding={finding}
                onChanged={onChanged}
                onOpenTab={onOpenTab}
              />
            ))}
          </ul>
        )}
      </Panel>

      <div className="grid gap-5 md:grid-cols-2">
        <Panel title="This machine">
          <Row label="Host">{base.host}</Row>
          <Row label="Kind">
            {base.kind === "vm"
              ? `local VM${base.vm_state ? ` (${base.vm_state})` : ""}`
              : "remote server"}
          </Row>
          <Row label="SSH">
            {base.ssh_user}@{base.host}:{base.ssh_port ?? 22}
          </Row>
          <Row label="Config repo">
            {status.config?.repo_url || base.config_repo_url ? (
              <span className="font-mono text-xs">
                {status.config?.repo_url || base.config_repo_url}
                {(status.config?.ref || base.config_ref) && (
                  <span className="text-zinc-500">
                    {" "}
                    ({status.config?.ref || base.config_ref})
                  </span>
                )}
              </span>
            ) : (
              <span className="text-zinc-500">not set up yet</span>
            )}
          </Row>
          <Row label="Status read" title={absolute(status.generated_at)}>
            {ago(status.generated_at)}
          </Row>
        </Panel>

        <Panel title="At a glance">
          <Row label="Services running">
            {services.length === 0 ? (
              <span className="text-zinc-500">none deployed</span>
            ) : (
              `${running} of ${services.length}`
            )}
          </Row>
          {unhealthy > 0 && (
            <Row label="Unhealthy">
              <span className="text-amber-300">{unhealthy}</span>
            </Row>
          )}
          <Row label="Last backup" title={absolute(status.security.last_backup)}>
            {ago(status.security.last_backup)}
          </Row>
          <Row label="Provably restorable">
            {status.security.backup_restorable ? (
              <span className="text-emerald-300">yes</span>
            ) : (
              <span className="text-amber-300">not yet verified</span>
            )}
          </Row>
          {status.security.disk_used_percent ? (
            <Row label="Disk used">{status.security.disk_used_percent}%</Row>
          ) : null}
          {status.security.cert_expiry_days ? (
            <Row label="Certificate expires">
              in {status.security.cert_expiry_days} days
            </Row>
          ) : null}
        </Panel>
      </div>

      <RemoveBase base={base} onRemoved={onRemoved} />
    </div>
  );
}

/**
 * One finding and the control that addresses it.
 *
 * The CLI decided the kind (`run` / `open` / `form` / `manual`); this
 * component only switches on it. `run` streams into a LogView. `open` jumps
 * to a tab. `form` expands an inline form that dry-runs then confirms. 
 * `manual` is plain text — a genuine dead-end with no in-app path.
 */
function FindingRow({
  base,
  finding,
  onChanged,
  onOpenTab,
}: {
  base: string;
  finding: Finding;
  onChanged: () => void;
  onOpenTab: (tab: Tab) => void;
}) {
  const [lines, setLines] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const action = finding.action ?? { kind: "manual" as const, label: "See command" };

  function finishStream(stream: { done: Promise<number> }) {
    stream.done
      .then((code) => {
        setBusy(false);
        if (code !== 0) {
          setError("The command did not finish cleanly. The output below is what the Base said.");
        }
        onChanged();
      })
      .catch((err: unknown) => {
        setBusy(false);
        setError(err instanceof Error ? err.message : String(err));
      });
  }

  function runAction() {
    if (!action.run) return;
    if (action.confirm && !window.confirm(action.confirm)) return;

    setBusy(true);
    setError(null);
    setLines([]);
    const collected: string[] = [];
    const onEvent = (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        collected.push(event.line);
        setLines([...collected]);
      }
    };

    if (action.run === "security scan") {
      void api
        .securityScan(base)
        .then((out) => {
          setLines(out.trim().split("\n").filter(Boolean));
          setBusy(false);
          onChanged();
        })
        .catch((err: unknown) => {
          setBusy(false);
          setError(err instanceof Error ? err.message : String(err));
        });
      return;
    }

    const streamers: Record<string, () => { done: Promise<number> }> = {
      "security fix": () => api.securityFix(base, onEvent),
      "security reboot": () => api.securityReboot(base, onEvent),
      "security install-scanner": () => api.installScanner(base, onEvent),
      "self-update": () => api.selfUpdate(base, onEvent),
      "upgrade --apply": () => api.upgradeApply(base, onEvent),
    };
    const start = streamers[action.run];
    if (!start) {
      setBusy(false);
      setError(`Unknown action: ${action.run}`);
      return;
    }
    finishStream(start());
  }

  return (
    <li className="rounded-lg border border-amber-500/20 bg-amber-500/5 px-3.5 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="text-sm text-amber-100/90">{finding.summary}</span>
        <span className="flex items-center gap-2">
          {action.kind === "run" && (
            <Button busy={busy} disabled={busy} onClick={runAction}>
              {action.label}
            </Button>
          )}
          {action.kind === "open" && action.tab && (
            <Button variant="secondary" onClick={() => onOpenTab(action.tab as Tab)}>
              {action.label}
            </Button>
          )}
          {action.kind === "form" && (
            <Button
              variant={formOpen ? "ghost" : "secondary"}
              onClick={() => setFormOpen((v) => !v)}
            >
              {formOpen ? "Cancel" : action.label}
            </Button>
          )}
          {action.kind === "manual" && (
            <span className="text-xs text-zinc-500">{finding.fix}</span>
          )}
        </span>
      </div>
      {action.kind === "form" && formOpen && action.form === "backup-setup" && (
        <div className="mt-3 border-t border-amber-500/10 pt-3">
          <BackupSetupForm
            base={base}
            onDone={() => {
              setFormOpen(false);
              onChanged();
            }}
          />
        </div>
      )}
      {action.kind === "form" && formOpen && action.form === "deploy" && action.service && (
        <div className="mt-3 border-t border-amber-500/10 pt-3">
          <DeployForm
            base={base}
            service={action.service}
            suggestedRef={action.suggested_ref || "main"}
            onDone={() => {
              setFormOpen(false);
              onChanged();
            }}
          />
        </div>
      )}
      {error && (
        <p className="mt-2 text-xs leading-relaxed text-red-300">{error}</p>
      )}
      {lines && lines.length > 0 && (
        <LogView lines={lines} className="mt-3 max-h-48 w-full" />
      )}
    </li>
  );
}

function DiffPreview({
  diff,
  commitMessage,
}: {
  diff: string;
  commitMessage: string;
}) {
  return (
    <div className="space-y-2">
      <p className="text-xs text-zinc-400">
        Commit: <span className="font-mono text-zinc-300">{commitMessage}</span>
      </p>
      <pre className="selectable max-h-56 overflow-auto rounded-md border border-zinc-800 bg-zinc-950/60 p-3 font-mono text-[11px] leading-relaxed text-zinc-300">
        {diff || "(no textual diff)"}
      </pre>
    </div>
  );
}

function DeployForm({
  base,
  service,
  suggestedRef,
  onDone,
}: {
  base: string;
  service: string;
  suggestedRef: string;
  onDone: () => void;
}) {
  const [ref, setRef] = useState(suggestedRef);
  const [preview, setPreview] = useState<import("../lib/types").ConfigPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  async function doPreview() {
    setBusy("preview");
    setError(null);
    setPreview(null);
    try {
      const p = await api.deployPreview(base, service, ref.trim() || "main");
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
      const out = await api.deploy(base, service, ref.trim() || "main");
      setResult(`Deployed ${out.service} at ${out.ref}.`);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-3">
      <Field label={`Ref for ${service}`} hint="Branch, tag, or commit SHA.">
        <Input
          value={ref}
          onChange={(e) => {
            setRef(e.target.value);
            setPreview(null);
          }}
          placeholder="main"
          spellCheck={false}
          disabled={busy !== null}
        />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="secondary"
          busy={busy === "preview"}
          disabled={busy !== null || !ref.trim()}
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
            {preview.would_change ? "Commit and deploy" : "Already current"}
          </Button>
        )}
      </div>
      {preview && (
        <DiffPreview diff={preview.diff} commitMessage={preview.commit_message} />
      )}
      {error && <p className="text-xs text-red-300">{error}</p>}
      {result && <p className="text-xs text-emerald-300">{result}</p>}
    </div>
  );
}

function BackupSetupForm({
  base,
  onDone,
}: {
  base: string;
  onDone: () => void;
}) {
  const [repo, setRepo] = useState("");
  const [password, setPassword] = useState("");
  const [awsKey, setAwsKey] = useState("");
  const [awsSecret, setAwsSecret] = useState("");
  const [preview, setPreview] = useState<import("../lib/types").ConfigPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const input = () => ({
    repo: repo.trim(),
    password,
    aws_access_key_id: awsKey || undefined,
    aws_secret_access_key: awsSecret || undefined,
  });

  async function doPreview() {
    if (!repo.trim()) return;
    setBusy("preview");
    setError(null);
    setPreview(null);
    try {
      const p = await api.backupSetupPreview(base, input());
      setPreview(p);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function doApply() {
    if (!preview || !password) return;
    if (
      !window.confirm(
        `Store backup credentials on the Base and commit this change to your config repo?\n\n${preview.commit_message}\n\nThe restic password is never recoverable from OwnBase — save it somewhere safe.`,
      )
    ) {
      return;
    }
    setBusy("apply");
    setError(null);
    try {
      const out = await api.backupSetupRun(base, input());
      setResult(out.trim() || "Backups configured.");
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-3">
      <Field
        label="Restic repository URL"
        hint="s3:…, b2:…, or sftp:… — off-machine, encrypted."
      >
        <Input
          value={repo}
          onChange={(e) => {
            setRepo(e.target.value);
            setPreview(null);
          }}
          placeholder="s3:s3.amazonaws.com/my-bucket/ownbase"
          spellCheck={false}
          disabled={busy !== null}
        />
      </Field>
      <Field
        label="Restic password"
        hint="Required to apply. Never recoverable from OwnBase — save it."
      >
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          disabled={busy !== null}
        />
      </Field>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="AWS access key (optional)">
          <Input
            value={awsKey}
            onChange={(e) => setAwsKey(e.target.value)}
            spellCheck={false}
            disabled={busy !== null}
          />
        </Field>
        <Field label="AWS secret key (optional)">
          <Input
            type="password"
            value={awsSecret}
            onChange={(e) => setAwsSecret(e.target.value)}
            autoComplete="off"
            disabled={busy !== null}
          />
        </Field>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="secondary"
          busy={busy === "preview"}
          disabled={busy !== null || !repo.trim()}
          onClick={() => void doPreview()}
        >
          Preview change
        </Button>
        {preview && (
          <Button
            busy={busy === "apply"}
            disabled={busy !== null || !password}
            onClick={() => void doApply()}
          >
            Confirm and set up
          </Button>
        )}
      </div>
      {preview && (
        <DiffPreview diff={preview.diff} commitMessage={preview.commit_message} />
      )}
      {error && <p className="text-xs text-red-300">{error}</p>}
      {result && (
        <LogView lines={result.split("\n")} className="max-h-48 w-full" />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Remove from this computer
// ---------------------------------------------------------------------------

/**
 * Forget a Base locally — vault profile + owner key — with an optional
 * Multipass destroy for local VMs. Never implies a remote cloud instance is
 * gone; that is the provider's console, not this button.
 */
function RemoveBase({
  base,
  onRemoved,
}: {
  base: BaseSummary;
  onRemoved: () => void;
}) {
  const isLocalVM = base.kind === "vm" || base.kind === "unregistered-vm";
  const hasProfile = base.registered || base.has_key;
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [destroyVM, setDestroyVM] = useState(base.kind === "unregistered-vm");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const ready = confirm.trim() === base.name;

  async function remove() {
    if (!ready) return;
    setBusy(true);
    setError(null);
    try {
      await api.deleteBase(base.name, {
        keepVm: isLocalVM ? !destroyVM : true,
      });
      // Parent navigates away; clear busy so Cancel is not stuck disabled if
      // the list reload is slow and this panel is still briefly mounted.
      setBusy(false);
      onRemoved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  const title =
    base.kind === "unregistered-vm"
      ? "Destroy this local VM"
      : "Remove from this computer";

  return (
    <Panel title={title}>
      {!open ? (
        <div className="space-y-3">
          <p className="text-sm leading-relaxed text-zinc-500">
            {base.kind === "unregistered-vm" ? (
              <>
                Deletes the Multipass VM named <strong className="text-zinc-300">{base.name}</strong>.
                There is no OwnBase profile for it.
              </>
            ) : (
              <>
                Removes the vault profile and owner key for{" "}
                <strong className="text-zinc-300">{base.name}</strong> from this computer.
                It does not uninstall OwnBase on the machine, delete the config repo,
                or destroy a cloud server — only what this laptop knows.
              </>
            )}
          </p>
          <Button
            variant="danger"
            onClick={() => {
              setOpen(true);
              setConfirm("");
              setError(null);
              setDestroyVM(base.kind === "unregistered-vm");
            }}
          >
            {title}…
          </Button>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm leading-relaxed text-zinc-400">
            {hasProfile && (
              <>
                This deletes the only client copy of the owner SSH key. Export anything
                you still need from the machine first — without that key you cannot log
                in again.
              </>
            )}
            {base.kind === "remote" && (
              <>
                {" "}
                The remote server keeps running and billing until you stop it at your
                provider or uninstall OwnBase on the box.
              </>
            )}
            {isLocalVM && destroyVM && hasProfile && (
              <> The local Multipass VM will be destroyed and its data lost.</>
            )}
            {isLocalVM && destroyVM && !hasProfile && (
              <> The Multipass VM will be destroyed and its data lost.</>
            )}
            {isLocalVM && !destroyVM && (
              <> The Multipass VM will be left running.</>
            )}
          </p>

          {isLocalVM && hasProfile && (
            <label className="flex cursor-pointer items-start gap-3 text-sm text-zinc-300">
              <input
                type="checkbox"
                className="mt-1"
                checked={destroyVM}
                onChange={(e) => setDestroyVM(e.target.checked)}
              />
              <span>
                Also destroy the local Multipass VM
                <span className="mt-0.5 block text-xs text-zinc-500">
                  All data on the VM is lost. Leave unchecked to only forget the profile.
                </span>
              </span>
            </label>
          )}

          <Field
            label={`Type ${base.name} to confirm`}
            hint="The name must match exactly."
          >
            <Input
              autoFocus
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              placeholder={base.name}
              spellCheck={false}
              autoCapitalize="off"
              disabled={busy}
            />
          </Field>

          {error && <ErrorNote title="Could not remove this Base" detail={error} />}

          <div className="flex flex-wrap gap-3">
            <Button variant="danger" busy={busy} disabled={!ready} onClick={() => void remove()}>
              {base.kind === "unregistered-vm"
                ? "Destroy VM"
                : destroyVM && isLocalVM
                  ? "Remove and destroy VM"
                  : "Remove from this computer"}
            </Button>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setOpen(false);
                setConfirm("");
                setError(null);
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

function Services({ status }: { status: BaseStatus }) {
  const services = status.services ?? [];
  const jobs = status.jobs ?? [];

  return (
    <div className="space-y-5">
      <Panel
        title="Services"
        subtitle="What ownbase.yaml asks for, and what the machine actually has running."
      >
        {services.length === 0 ? (
          <Unavailable>
            Nothing is deployed yet. Services are declared in{" "}
            <code className="font-mono text-xs">ownbase.yaml</code> in your config
            repo, and appear here after the Base reconciles.
          </Unavailable>
        ) : (
          <ul className="divide-y divide-zinc-800">
            {services.map((service) => (
              <ServiceRow key={service.name} service={service} />
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
                  <span className="flex items-center gap-2 text-sm text-zinc-200">
                    <Dot tone={job.timer_enabled ? "good" : "bad"} />
                    {job.name}
                  </span>
                  <span className="font-mono text-xs text-zinc-500">{job.schedule}</span>
                </div>
                <p className="mt-1 pl-4 text-xs text-zinc-500">
                  reuses {job.service}
                  {job.last_run && (
                    <>
                      {" · last run "}
                      <span title={absolute(job.last_run)}>{ago(job.last_run)}</span>
                      {job.last_result ? ` (${job.last_result})` : ""}
                    </>
                  )}
                  {job.next_run && ` · next ${until(job.next_run)}`}
                  {!job.timer_enabled && " · timer not enabled"}
                </p>
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}

function ServiceRow({ service }: { service: ServiceStatus }) {
  const tone: Tone = !service.running ? "bad" : service.healthy ? "good" : "warn";
  const state = !service.running
    ? "stopped"
    : service.healthy
      ? "running"
      : "running, unhealthy";
  const domains = service.domains ?? (service.domain ? [service.domain] : []);

  return (
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="flex items-center gap-2 text-sm text-zinc-200">
          <Dot tone={tone} />
          {service.name}
        </span>
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
        </span>
      </div>
      {(domains.length > 0 || service.repo || service.health_probe_result) && (
        <div className="mt-1 space-y-0.5 pl-4 text-xs text-zinc-500">
          {domains.map((domain) => (
            <p key={domain} className="selectable font-mono">
              https://{domain}
            </p>
          ))}
          {service.repo && <p className="selectable font-mono">{service.repo}</p>}
          {service.health_probe_result && <p>probe: {service.health_probe_result}</p>}
        </div>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

function Security({
  base,
  status,
  onChanged,
}: {
  base: BaseSummary;
  status: BaseStatus;
  onChanged: () => void;
}) {
  const {
    exposure,
    access,
    vulns,
    drift_detected,
    drift_count,
    drift_files,
    reboot_required,
    reboot_packages,
  } = status.security;

  return (
    <div className="space-y-5">
      {reboot_required && (
        <Panel
          title="Reboot required"
          subtitle="Applied packages need a reboot to take effect — usually a new kernel."
          action={<RebootAction base={base.name} onChanged={onChanged} />}
        >
          <p className="text-sm leading-relaxed text-zinc-400">
            Until the machine restarts, the CVE scan can report clean while the
            still-running kernel is the vulnerable one. Every service will drop
            for about 30–60 seconds.
          </p>
          {reboot_packages && reboot_packages.length > 0 && (
            <p className="selectable mt-2 font-mono text-xs leading-relaxed text-zinc-500">
              {reboot_packages.join("  ")}
            </p>
          )}
        </Panel>
      )}

      <Panel
        title="Network exposure"
        subtitle="What this machine believes is reachable from the internet."
      >
        {!exposure.available ? (
          <Unavailable>
            No exposure inventory yet. The Base probes with{" "}
            <code className="font-mono">ss</code> and{" "}
            <code className="font-mono">ufw</code> after each reconcile — treat this as
            unknown rather than clear until the first probe lands.
          </Unavailable>
        ) : (
          <>
            <Row label="Firewall">
              {exposure.firewall_active ? (
                <span className="text-emerald-300">active</span>
              ) : (
                <span className="text-red-300">not active</span>
              )}
            </Row>
            <Row label="Unexpected open ports">
              {exposure.unexpected_count === 0 ? (
                <span className="text-emerald-300">none</span>
              ) : (
                <span className="text-amber-300">{exposure.unexpected_count}</span>
              )}
            </Row>
            {exposure.listeners && exposure.listeners.length > 0 && (
              <ul className="mt-3 divide-y divide-zinc-800 border-t border-zinc-800">
                {exposure.listeners.map((l) => (
                  <li
                    key={`${l.proto}-${l.bind}-${l.port}`}
                    className="flex items-center justify-between gap-3 py-2 text-xs"
                  >
                    <span className="selectable font-mono text-zinc-300">
                      {l.bind}:{l.port}/{l.proto}
                      {l.process ? ` ${l.process}` : ""}
                    </span>
                    <span className="flex items-center gap-2">
                      {l.internet_reachable ? (
                        <Badge tone={l.expected ? "info" : "bad"}>
                          {l.expected ? "public, expected" : "public, unexpected"}
                        </Badge>
                      ) : (
                        <Badge tone="good">loopback only</Badge>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="mt-3 text-xs leading-relaxed text-zinc-500">
              This is the on-machine view. It cannot see a firewall your provider
              runs in front of the machine, or a kernel hiding a socket from it.
            </p>
          </>
        )}
      </Panel>

      <Panel title="SSH access" subtitle="Who got in, and who kept failing to.">
        {!access.available ? (
          <Unavailable>
            No SSH access data yet. The Base reads fail2ban and the journal after each
            reconcile — treat this as unknown rather than clear until the first probe
            lands.
          </Unavailable>
        ) : (
          <>
            <Row label="Brute-force protection">
              {!access.fail2ban_available ? (
                <span className="text-zinc-500">unknown</span>
              ) : access.fail2ban_active ? (
                <span className="text-emerald-300">active</span>
              ) : (
                <span className="text-amber-300">not active</span>
              )}
            </Row>
            <Row label="Failed attempts">{access.failed_attempts}</Row>
            <Row label="Currently banned">{access.banned_ips?.length ?? 0}</Row>
            {access.banned_ips && access.banned_ips.length > 0 && (
              <p className="selectable mt-2 font-mono text-xs leading-relaxed text-zinc-500">
                {access.banned_ips.join("  ")}
              </p>
            )}
            {access.recent_logins && access.recent_logins.length > 0 && (
              <ul className="mt-3 divide-y divide-zinc-800 border-t border-zinc-800">
                {access.recent_logins.map((login, i) => (
                  <li
                    key={`${login.time}-${i}`}
                    className="flex items-center justify-between gap-3 py-2 text-xs"
                  >
                    <span className="selectable font-mono text-zinc-300">
                      {login.user}@{login.source_ip}
                      {login.method ? ` (${login.method})` : ""}
                    </span>
                    <span className="text-zinc-500" title={absolute(login.time)}>
                      {ago(login.time)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </Panel>

      <Panel
        title="Known vulnerabilities"
        subtitle={
          vulns.scanned_at
            ? `Last scanned ${ago(vulns.scanned_at)}. Only CVEs with a published patch are actionable.`
            : "Scanned daily by the Base. Only CVEs with a published patch are actionable."
        }
        action={<RescanAction base={base.name} onChanged={onChanged} />}
      >
        {!vulns.available ? (
          <Unavailable>
            {vulns.trivy_installed
              ? `The scanner ran but the host scan failed${vulns.host_scan_error ? `: ${vulns.host_scan_error}` : ""}. That is unknown, not clean.`
              : "No scanner on this machine yet, so nothing has been checked. That is unknown, not clean."}
          </Unavailable>
        ) : (
          <>
            <VulnTarget
              label="Host OS"
              summary={vulns.host}
              defaultOpen
              fixHint={
                (vulns.host.fixable_critical ?? 0) + (vulns.host.fixable_high ?? 0) > 0
                  ? `Apply host patches with ownbasectl security fix ${base.name}`
                  : undefined
              }
            />
            {vulns.images?.map((image) =>
              image.scan_failed ? (
                <Row key={image.service} label={image.service} title={image.image}>
                  <span className="text-amber-300">
                    scan failed{image.scan_error ? ` — ${image.scan_error}` : ""}
                  </span>
                </Row>
              ) : (
                <VulnTarget
                  key={image.service}
                  label={image.service}
                  title={image.image}
                  summary={image.summary}
                />
              ),
            )}
            <p className="mt-3 text-xs leading-relaxed text-zinc-500">
              A CVE with no published patch clears when Ubuntu (or the image
              upstream) ships one — there is nothing to run in the meantime.
              Host packages with a patch, core-image CVEs, and service updates
              are actionable from Overview.
            </p>
          </>
        )}
      </Panel>

      <Panel
        title="Tampering"
        subtitle="The generated files on the Base have exactly one writer: its daemon."
      >
        {!drift_detected ? (
          <Unavailable>
            Nothing has drifted. Every generated file matches what the compiler
            produced from your config.
          </Unavailable>
        ) : (
          <>
            <Row label="Files changed outside the daemon">
              <span className="text-red-300">{drift_count ?? 0}</span>
            </Row>
            {drift_files && (
              <p className="selectable mt-2 font-mono text-xs leading-relaxed text-zinc-400">
                {drift_files.join("\n")}
              </p>
            )}
            <p className="mt-3 text-xs leading-relaxed text-zinc-500">
              Any difference is a signal worth understanding. The daemon is the
              only writer of generated files — a drift means something else
              changed them.
            </p>
          </>
        )}
      </Panel>
    </div>
  );
}

function Severities({ summary }: { summary: VulnSummary }) {
  const total = summary.critical + summary.high + summary.medium + summary.low;
  if (total === 0) return <span className="text-emerald-300">none found</span>;
  const fixable =
    (summary.fixable_critical ?? 0) + (summary.fixable_high ?? 0);
  const unfixed =
    summary.critical + summary.high - fixable;
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      {summary.critical > 0 && <Badge tone="bad">{summary.critical} critical</Badge>}
      {summary.high > 0 && <Badge tone="warn">{summary.high} high</Badge>}
      <span className="text-xs text-zinc-500">
        {summary.medium + summary.low} lower
        {summary.critical + summary.high > 0 && (
          <>
            {" · "}
            {fixable > 0 ? (
              <span className="text-amber-300">{fixable} with a patch</span>
            ) : (
              "none with a patch"
            )}
            {unfixed > 0 && <> · {unfixed} with none published</>}
          </>
        )}
      </span>
    </span>
  );
}

/** One scan target (host or image) with an expandable top-findings list. */
function VulnTarget({
  label,
  title,
  summary,
  defaultOpen = false,
  fixHint,
}: {
  label: string;
  title?: string;
  summary: VulnSummary;
  defaultOpen?: boolean;
  fixHint?: string;
}) {
  const top = summary.top ?? [];
  const [open, setOpen] = useState(defaultOpen && top.length > 0);
  const hasTop = top.length > 0;

  return (
    <div className="border-b border-zinc-800 py-2 last:border-b-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <button
          type="button"
          className={cx(
            "flex min-w-0 items-center gap-2 text-left text-sm text-zinc-200",
            hasTop && "cursor-pointer hover:text-zinc-50",
          )}
          onClick={() => hasTop && setOpen((v) => !v)}
          disabled={!hasTop}
          title={title}
        >
          {hasTop && (
            <span className="font-mono text-xs text-zinc-500">{open ? "▾" : "▸"}</span>
          )}
          <span className="truncate">{label}</span>
        </button>
        <Severities summary={summary} />
      </div>
      {open && hasTop && (
        <div className="mt-2">
          <VulnList findings={top} />
          {top.length >= 20 && (
            <p className="mt-1 text-xs text-zinc-500">
              Showing the 20 most severe. Medium and low are counted above but not listed.
            </p>
          )}
          {fixHint && (
            <p className="mt-2 text-xs text-zinc-500">{fixHint}</p>
          )}
        </div>
      )}
    </div>
  );
}

function VulnList({
  findings,
}: {
  findings: NonNullable<VulnSummary["top"]>;
}) {
  return (
    <ul className="divide-y divide-zinc-800 border-t border-zinc-800">
      {findings.map((finding) => (
        <li key={`${finding.vuln_id}-${finding.package}`} className="py-2 text-xs">
          <div className="flex items-center justify-between gap-3">
            <span className="selectable font-mono text-zinc-300">{finding.vuln_id}</span>
            <Badge tone={finding.severity.toUpperCase() === "CRITICAL" ? "bad" : "warn"}>
              {finding.severity.toLowerCase()}
            </Badge>
          </div>
          <p className="mt-0.5 text-zinc-500">
            {finding.package}
            {finding.version ? ` ${finding.version}` : ""}
            {finding.fixed_in
              ? ` → fixed in ${finding.fixed_in}`
              : " — no fix published yet"}
          </p>
        </li>
      ))}
    </ul>
  );
}

function RescanAction({ base, onChanged }: { base: string; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  async function rescan() {
    setBusy(true);
    setMsg(null);
    try {
      const out = await api.securityScan(base);
      setMsg(out.trim() || "Scan started.");
      onChanged();
    } catch (err) {
      setMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <Button variant="secondary" busy={busy} onClick={() => void rescan()}>
        Rescan
      </Button>
      {msg && <p className="max-w-xs text-right text-xs text-zinc-500">{msg}</p>}
    </div>
  );
}

function RebootAction({ base, onChanged }: { base: string; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [lines, setLines] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  function reboot() {
    if (
      !window.confirm(
        "Every service on this Base will stop and restart with the machine. The outage is typically 30–60 seconds. Reboot now?",
      )
    ) {
      return;
    }
    setBusy(true);
    setError(null);
    setLines([]);
    const collected: string[] = [];
    const stream = api.securityReboot(base, (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        collected.push(event.line);
        setLines([...collected]);
      }
    });
    stream.done
      .then((code) => {
        setBusy(false);
        if (code !== 0) {
          setError("The reboot command did not finish cleanly.");
        }
        onChanged();
      })
      .catch((err: unknown) => {
        setBusy(false);
        setError(err instanceof Error ? err.message : String(err));
      });
  }

  return (
    <div className="flex flex-col items-end gap-2">
      <Button variant="danger" busy={busy} onClick={reboot}>
        Reboot now
      </Button>
      {error && <p className="max-w-xs text-right text-xs text-red-300">{error}</p>}
      {lines && lines.length > 0 && (
        <LogView lines={lines} className="max-h-32 w-full min-w-[20rem]" />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Backups
// ---------------------------------------------------------------------------

function Backups({
  base,
  status,
  onChanged,
}: {
  base: BaseSummary;
  status: BaseStatus;
  onChanged: () => void;
}) {
  const { last_backup, last_verified, backup_restorable } = status.security;
  const configured = Boolean(last_backup);

  return (
    <div className="space-y-5">
      <Panel
        title="Backups"
        subtitle="A backup nobody has restored is a hope, not a backup."
        action={
          configured ? <BackupActions base={base.name} onChanged={onChanged} /> : undefined
        }
      >
        {!configured ? (
          <div className="space-y-4">
            <p className="text-sm leading-relaxed text-zinc-400">
              No snapshot has ever been taken, so there is no way back from a lost
              disk. Backups go to an encrypted off-machine repository you own
              (S3, B2, or SFTP).
            </p>
            <BackupSetupForm base={base.name} onDone={onChanged} />
          </div>
        ) : (
          <>
            <Row label="Last snapshot" title={absolute(last_backup)}>
              {ago(last_backup)}
            </Row>
            <Row label="Provably restorable">
              {backup_restorable ? (
                <span className="text-emerald-300">yes</span>
              ) : (
                <span className="text-amber-300">not yet verified</span>
              )}
            </Row>
            <Row label="Last restore drill" title={absolute(last_verified)}>
              {ago(last_verified)}
            </Row>
            <p className="mt-3 text-xs leading-relaxed text-zinc-500">
              The drill is the part that matters: the Base restores its newest
              snapshot into an isolated directory, checks it, and when Postgres is in
              the backup starts a real database from it and waits for recovery. Until
              that has passed, "restorable" is an assumption.
            </p>
          </>
        )}
      </Panel>
    </div>
  );
}

/**
 * The two backup actions the app can safely take.
 *
 * Neither changes what the Base is supposed to be running, which is why they
 * belong in a read-only window: one takes a snapshot, the other proves a
 * snapshot can be restored.
 */
function BackupActions({ base, onChanged }: { base: string; onChanged: () => void }) {
  const [lines, setLines] = useState<string[] | null>(null);
  const [busy, setBusy] = useState<"backup" | "verify" | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function snapshot() {
    setBusy("backup");
    setError(null);
    setLines(null);
    try {
      const out = await api.backupNow(base);
      setLines(out.trim().split("\n"));
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  function verify() {
    setBusy("verify");
    setError(null);
    setLines([]);
    const collected: string[] = [];
    const stream = api.verifyBackup(base, (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        collected.push(event.line);
        setLines([...collected]);
      }
    });
    stream.done
      .then((code) => {
        setBusy(null);
        if (code !== 0) {
          setError(
            "The drill did not pass. The output below names the check that failed — that is the specific reason this Base is not provably restorable.",
          );
        }
        onChanged();
      })
      .catch((err: unknown) => {
        setBusy(null);
        setError(err instanceof Error ? err.message : String(err));
      });
  }

  return (
    <div className="flex flex-col items-end gap-3">
      <div className="flex gap-2">
        <Button busy={busy === "backup"} disabled={busy !== null} onClick={snapshot}>
          Back up now
        </Button>
        <Button busy={busy === "verify"} disabled={busy !== null} onClick={verify}>
          Run the restore drill
        </Button>
      </div>
      {busy === "verify" && (
        <p className="text-xs text-zinc-500">
          This takes minutes — it is a real restore.
        </p>
      )}
      {error && (
        <p className="max-w-md text-right text-xs leading-relaxed text-red-300">
          {error}
        </p>
      )}
      {lines && <LogView lines={lines} className="max-h-48 w-full min-w-[24rem]" />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Updates
// ---------------------------------------------------------------------------

function Updates({
  base,
  status,
  onChanged,
}: {
  base: BaseSummary;
  status: BaseStatus;
  onChanged: () => void;
}) {
  const drift = status.updates.drift ?? [];
  const [openFor, setOpenFor] = useState<string | null>(null);

  return (
    <Panel
      title="Service updates"
      subtitle="How far each service is from its source repo. Updating commits a pin to your config repo after you confirm the diff."
    >
      {drift.length === 0 ? (
        <Unavailable>
          Nothing to compare yet. The Base checks each service's source repo on its
          own schedule and reports what it finds here.
        </Unavailable>
      ) : (
        <ul className="divide-y divide-zinc-800">
          {drift.map((d) => (
            <li key={d.service} className="py-3 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className="flex items-center gap-2 text-sm text-zinc-200">
                  <Dot tone={d.up_to_date ? "good" : "warn"} />
                  {d.service}
                </span>
                <span className="flex items-center gap-3">
                  <span className="font-mono text-xs text-zinc-500" title={d.ref}>
                    @{shortRef(d.ref)}
                  </span>
                  {!d.up_to_date && (
                    <Button
                      variant="secondary"
                      onClick={() =>
                        setOpenFor((cur) => (cur === d.service ? null : d.service))
                      }
                    >
                      {openFor === d.service ? "Cancel" : "Update"}
                    </Button>
                  )}
                </span>
              </div>
              <p className="mt-1 pl-4 text-xs text-zinc-500">
                {d.up_to_date
                  ? "up to date"
                  : `${d.commits_behind} commit${d.commits_behind === 1 ? "" : "s"} behind ${d.branch || "the default branch"}`}
                {d.newest_tag && ` · newest tag ${d.newest_tag}`}
              </p>
              {openFor === d.service && (
                <div className="mt-3 pl-4">
                  <DeployForm
                    base={base.name}
                    service={d.service}
                    suggestedRef={d.newest_tag || d.branch || "main"}
                    onDone={() => {
                      setOpenFor(null);
                      onChanged();
                    }}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// Activity
// ---------------------------------------------------------------------------

function Activity({ status }: { status: BaseStatus }) {
  const actions = status.audit.recent_actions ?? [];

  return (
    <Panel
      title="Recent actions"
      subtitle="From the Base's own audit log — every governed action it took."
    >
      {actions.length === 0 ? (
        <Unavailable>Nothing recorded yet.</Unavailable>
      ) : (
        <ul className="divide-y divide-zinc-800">
          {actions.map((action, i) => (
            <li
              key={`${action.time}-${i}`}
              className="flex flex-wrap items-center justify-between gap-3 py-2.5 text-xs first:pt-0 last:pb-0"
            >
              <span className="flex min-w-0 items-center gap-2">
                <Dot tone={action.outcome === "applied" ? "good" : "bad"} />
                <span className="font-mono text-zinc-300">{action.action}</span>
                {action.target && (
                  <span className="selectable truncate text-zinc-500">
                    {action.target}
                  </span>
                )}
              </span>
              <span className="text-zinc-500" title={absolute(action.time)}>
                {ago(action.time)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {status.audit.total_seen > actions.length && (
        <p className="mt-3 text-xs text-zinc-500">
          Showing the most recent {actions.length} of {status.audit.total_seen}.
        </p>
      )}
    </Panel>
  );
}