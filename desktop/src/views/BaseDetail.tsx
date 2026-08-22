import { useCallback, useState } from "react";
import { RefreshCw, Shield, Trash2 } from "lucide-react";

import { CopyButton } from "../components/CopyButton";
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
import { absolute, ago, pickDeployRef, shortRef } from "../lib/format";
import type {
  BaseStatus,
  BaseSummary,
  Checkup,
  Finding,
  ServiceDrift,
  VulnSummary,
} from "../lib/types";
import { useAsync } from "../lib/useAsync";

import { DiffPreview } from "./DiffPreview";
import { ServicePanel } from "./ServicePanel";

type Tab = "overview" | "services" | "security" | "backups" | "updates" | "activity";

/**
 * Everything known about one Base.
 *
 * Config changes go through the same client-side path as the CLI (clone, edit,
 * commit, push) and always dry-run → confirm first. Host actions (patches,
 * reboot, scanner, self-update, upgrade) never touch git. Secret values are
 * never shown until the operator reveals one key.
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
      <Header
        base={base}
        findings={findings}
        state={state}
        daemonVersion={status?.version}
      />

      <div className="border-b border-line px-8">
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
          <div className="flex items-center gap-3 text-sm text-fg-subtle">
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
      return (
        <ServicePanel base={base.name} status={status} onChanged={onChanged} />
      );
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
  daemonVersion,
}: {
  base: BaseSummary;
  findings: Finding[];
  state: { loading: boolean; refreshing: boolean; error: string | null; reload: () => void };
  daemonVersion?: string;
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
          <h1 className="truncate text-2xl font-semibold tracking-tight text-fg">
            {base.name}
          </h1>
          <Badge tone={verdict.tone}>
            <Dot tone={verdict.tone} />
            {verdict.text}
          </Badge>
        </div>
        <p className="selectable mt-1 truncate font-mono text-xs text-fg-subtle">
          {base.ssh_user}@{base.host}
          {base.ssh_port && base.ssh_port !== 22 ? `:${base.ssh_port}` : ""}
          {daemonVersion ? ` · ownbased ${daemonVersion}` : ""}
        </p>
      </div>
      <Button variant="secondary" busy={state.refreshing} onClick={state.reload}>
        <RefreshCw className="h-3.5 w-3.5" aria-hidden />
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
      <h1 className="text-2xl font-semibold tracking-tight text-fg">{base.name}</h1>
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
      <p className="text-sm leading-relaxed text-fg-subtle">
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
                  <span className="text-fg-subtle">
                    {" "}
                    ({status.config?.ref || base.config_ref})
                  </span>
                )}
              </span>
            ) : (
              <span className="text-fg-subtle">not set up yet</span>
            )}
          </Row>
          <Row label="Status read" title={absolute(status.generated_at)}>
            {ago(status.generated_at)}
          </Row>
        </Panel>

        <Panel title="At a glance">
          <Row label="Services running">
            {services.length === 0 ? (
              <span className="text-fg-subtle">none deployed</span>
            ) : (
              `${running} of ${services.length}`
            )}
          </Row>
          {unhealthy > 0 && (
            <Row label="Unhealthy">
              <span className="text-warn-fg">{unhealthy}</span>
            </Row>
          )}
          <Row label="Last backup" title={absolute(status.security.last_backup)}>
            {ago(status.security.last_backup)}
          </Row>
          <Row label="Provably restorable">
            {status.security.backup_restorable ? (
              <span className="text-good-fg">yes</span>
            ) : (
              <span className="text-warn-fg">not yet verified</span>
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
      "security fix --reboot": () => api.securityFix(base, onEvent, { reboot: true }),
      "security reboot": () => api.securityReboot(base, onEvent),
      "security reboot --wait": () => api.securityReboot(base, onEvent, { wait: true }),
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
    <li className="rounded-lg border border-line bg-surface px-3.5 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="flex min-w-0 items-start gap-2.5 text-sm text-fg">
          <Shield className="mt-0.5 h-4 w-4 shrink-0 text-accent" aria-hidden />
          <span>{finding.summary}</span>
        </span>
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
            <span className="text-xs text-fg-subtle">{finding.fix}</span>
          )}
        </span>
      </div>
      {action.kind === "form" && formOpen && action.form === "backup-setup" && (
        <div className="mt-3 border-t border-line pt-3">
          <BackupSetupForm
            base={base}
            onDone={() => {
              setFormOpen(false);
              onChanged();
            }}
          />
        </div>
      )}
      {action.kind === "form" && formOpen && action.form === "config-setup" && (
        <div className="mt-3 border-t border-line pt-3">
          <ConfigSetupForm
            base={base}
            onDone={() => {
              setFormOpen(false);
              onChanged();
            }}
          />
        </div>
      )}
      {action.kind === "form" && formOpen && action.form === "deploy" && action.service && (
        <div className="mt-3 border-t border-line pt-3">
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
        <p className="mt-2 text-xs leading-relaxed text-bad-fg">{error}</p>
      )}
      {lines && lines.length > 0 && (
        <LogView lines={lines} className="mt-3 max-h-48 w-full" />
      )}
    </li>
  );
}

/**
 * Point a Base at its external config repo.
 *
 * Order: deploy key (so the Base can clone) → paste the public key on the
 * host → enter the git URL → optional seed → config setup.
 */
export function ConfigSetupForm({
  base,
  onDone,
}: {
  base: string;
  onDone: () => void;
}) {
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("main");
  const [init, setInit] = useState(true);
  const [publicKey, setPublicKey] = useState<string | null>(null);
  const [busy, setBusy] = useState<"key" | "setup" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  async function loadKey() {
    setBusy("key");
    setError(null);
    try {
      // Prefer an existing key; only generate when none is registered.
      try {
        const existing = await api.sshKeyList(base);
        if (existing.public_key) {
          setPublicKey(existing.public_key);
          return;
        }
      } catch {
        /* none yet */
      }
      const host = repo.includes("gitlab")
        ? "gitlab.com"
        : repo.includes("bitbucket")
          ? "bitbucket.org"
          : "github.com";
      const r = await api.sshKeyAdd(base, host);
      setPublicKey(r.public_key);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function runSetup() {
    if (!repo.trim()) return;
    setBusy("setup");
    setError(null);
    try {
      await api.configSetup(base, {
        repo: repo.trim(),
        ref: ref.trim() || "main",
        init,
      });
      setDone(true);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-xs leading-relaxed text-fg-subtle">
        What runs on this Base is decided in a git repo you own. The Base clones
        it read-only; you commit changes from this computer.
      </p>
      <Field label="Config repo URL" hint="git@github.com:you/mybase-config.git">
        <Input
          value={repo}
          onChange={(e) => setRepo(e.target.value)}
          placeholder="git@github.com:you/mybase-config.git"
          spellCheck={false}
          disabled={busy !== null || done}
        />
      </Field>
      <Field label="Branch" hint="Usually main.">
        <Input
          value={ref}
          onChange={(e) => setRef(e.target.value)}
          placeholder="main"
          spellCheck={false}
          disabled={busy !== null || done}
        />
      </Field>
      <label className="flex cursor-pointer items-start gap-3 text-sm text-fg-muted">
        <input
          type="checkbox"
          className="mt-1"
          checked={init}
          onChange={(e) => setInit(e.target.checked)}
          disabled={busy !== null || done}
        />
        <span>
          Seed a starter ownbase.yaml if the repo is empty
          <span className="mt-0.5 block text-xs text-fg-subtle">
            Postgres with point-in-time recovery. Safe to uncheck if the repo
            already has a config.
          </span>
        </span>
      </label>

      <div className="space-y-2 rounded-md border border-line bg-surface-sunken p-3">
        <p className="text-xs font-medium text-fg-muted">
          1. Register the Base&apos;s deploy key on the repo
        </p>
        <p className="text-xs leading-relaxed text-fg-subtle">
          Read-only. The Base uses this key to clone — different from the owner
          key you use to SSH in.
        </p>
        {!publicKey ? (
          <Button
            variant="secondary"
            busy={busy === "key"}
            disabled={busy !== null || done}
            onClick={() => void loadKey()}
          >
            Generate deploy key
          </Button>
        ) : (
          <div className="space-y-2">
            <p className="selectable break-all font-mono text-[11px] text-fg-muted">
              {publicKey}
            </p>
            <CopyButton value={publicKey} label="Copy public key" />
          </div>
        )}
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          busy={busy === "setup"}
          disabled={busy !== null || done || !repo.trim()}
          onClick={() => void runSetup()}
        >
          {done ? "Configured" : "Point Base at this repo"}
        </Button>
      </div>
      {error && <p className="text-xs text-bad-fg">{error}</p>}
      {done && (
        <p className="text-xs text-good-fg">
          Config source set. The Base is pulling and reconciling.
        </p>
      )}
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
      {error && <p className="text-xs text-bad-fg">{error}</p>}
      {result && <p className="text-xs text-good-fg">{result}</p>}
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
  const [b2AccountID, setB2AccountID] = useState("");
  const [b2AccountKey, setB2AccountKey] = useState("");
  const [preview, setPreview] = useState<import("../lib/types").ConfigPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const isB2 = repo.trim().toLowerCase().startsWith("b2:");
  const isS3 = repo.trim().toLowerCase().startsWith("s3:");

  const input = () => ({
    repo: repo.trim(),
    password,
    aws_access_key_id: awsKey || undefined,
    aws_secret_access_key: awsSecret || undefined,
    b2_account_id: b2AccountID || undefined,
    b2_account_key: b2AccountKey || undefined,
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
    if (!preview.would_change) {
      // Still store secrets + run first backup, but do not claim a git commit.
      if (
        !window.confirm(
          "ownbase.yaml already has this backup configuration. Store credentials on the Base and run the first snapshot?",
        )
      ) {
        return;
      }
    } else if (
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
      {(isS3 || (!isB2 && !repo.trim())) && (
        <div className="grid gap-3 md:grid-cols-2">
          <Field label="AWS access key (for s3: repos)">
            <Input
              value={awsKey}
              onChange={(e) => setAwsKey(e.target.value)}
              spellCheck={false}
              disabled={busy !== null}
            />
          </Field>
          <Field label="AWS secret key (for s3: repos)">
            <Input
              type="password"
              value={awsSecret}
              onChange={(e) => setAwsSecret(e.target.value)}
              autoComplete="off"
              disabled={busy !== null}
            />
          </Field>
        </div>
      )}
      {(isB2 || (!isS3 && !repo.trim())) && (
        <div className="grid gap-3 md:grid-cols-2">
          <Field label="B2 account ID (for b2: repos)">
            <Input
              value={b2AccountID}
              onChange={(e) => setB2AccountID(e.target.value)}
              spellCheck={false}
              disabled={busy !== null}
            />
          </Field>
          <Field label="B2 application key (for b2: repos)">
            <Input
              type="password"
              value={b2AccountKey}
              onChange={(e) => setB2AccountKey(e.target.value)}
              autoComplete="off"
              disabled={busy !== null}
            />
          </Field>
        </div>
      )}
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
            {preview.would_change ? "Confirm and set up" : "Store credentials and back up"}
          </Button>
        )}
      </div>
      {preview && (
        <DiffPreview diff={preview.diff} commitMessage={preview.commit_message} />
      )}
      {error && <p className="text-xs text-bad-fg">{error}</p>}
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
          <p className="text-sm leading-relaxed text-fg-subtle">
            {base.kind === "unregistered-vm" ? (
              <>
                Deletes the Multipass VM named <strong className="text-fg-muted">{base.name}</strong>.
                There is no OwnBase profile for it.
              </>
            ) : (
              <>
                Removes the vault profile and owner key for{" "}
                <strong className="text-fg-muted">{base.name}</strong> from this computer.
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
          <p className="text-sm leading-relaxed text-fg-muted">
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
            <label className="flex cursor-pointer items-start gap-3 text-sm text-fg-muted">
              <input
                type="checkbox"
                className="mt-1"
                checked={destroyVM}
                onChange={(e) => setDestroyVM(e.target.checked)}
              />
              <span>
                Also destroy the local Multipass VM
                <span className="mt-0.5 block text-xs text-fg-subtle">
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
              <Trash2 className="h-3.5 w-3.5" aria-hidden />
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
          subtitle="Applied packages need a reboot to take effect — usually a new kernel. A CVE rescan runs automatically on boot."
          action={<RebootAction base={base.name} onChanged={onChanged} />}
        >
          <p className="text-sm leading-relaxed text-fg-muted">
            Until the machine restarts, the CVE scan can report clean while the
            still-running kernel is the vulnerable one. Every service will drop
            for about 30–60 seconds.
          </p>
          {reboot_packages && reboot_packages.length > 0 && (
            <p className="selectable mt-2 font-mono text-xs leading-relaxed text-fg-subtle">
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
                <span className="text-good-fg">active</span>
              ) : (
                <span className="text-bad-fg">not active</span>
              )}
            </Row>
            <Row label="Unexpected open ports">
              {exposure.unexpected_count === 0 ? (
                <span className="text-good-fg">none</span>
              ) : (
                <span className="text-warn-fg">{exposure.unexpected_count}</span>
              )}
            </Row>
            {exposure.listeners && exposure.listeners.length > 0 && (
              <ul className="mt-3 divide-y divide-line border-t border-line">
                {exposure.listeners.map((l) => (
                  <li
                    key={`${l.proto}-${l.bind}-${l.port}`}
                    className="flex items-center justify-between gap-3 py-2 text-xs"
                  >
                    <span className="selectable font-mono text-fg-muted">
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
            <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
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
                <span className="text-fg-subtle">unknown</span>
              ) : access.fail2ban_active ? (
                <span className="text-good-fg">active</span>
              ) : (
                <span className="text-warn-fg">not active</span>
              )}
            </Row>
            <Row label="Failed attempts">{access.failed_attempts}</Row>
            <Row label="Currently banned">{access.banned_ips?.length ?? 0}</Row>
            {access.banned_ips && access.banned_ips.length > 0 && (
              <p className="selectable mt-2 font-mono text-xs leading-relaxed text-fg-subtle">
                {access.banned_ips.join("  ")}
              </p>
            )}
            {access.recent_logins && access.recent_logins.length > 0 && (
              <ul className="mt-3 divide-y divide-line border-t border-line">
                {access.recent_logins.map((login, i) => (
                  <li
                    key={`${login.time}-${i}`}
                    className="flex items-center justify-between gap-3 py-2 text-xs"
                  >
                    <span className="selectable font-mono text-fg-muted">
                      {login.user}@{login.source_ip}
                      {login.method ? ` (${login.method})` : ""}
                    </span>
                    <span className="text-fg-subtle" title={absolute(login.time)}>
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
          vulns.scanning
            ? `Scan in progress${vulns.scan_started_at ? ` (started ${ago(vulns.scan_started_at)})` : ""}. Counts below may be from the previous scan.`
            : vulns.scanned_at
              ? `Last scanned ${ago(vulns.scanned_at)}. Only CVEs with a published patch are actionable.`
              : "Scanned daily by the Base. Only CVEs with a published patch are actionable."
        }
        action={<RescanAction base={base.name} onChanged={onChanged} scanning={!!vulns.scanning} />}
      >
        {vulns.scanning && !vulns.available ? (
          <Unavailable>
            A CVE scan is running. Results will land here when it finishes —
            unknown until then, not clean.
          </Unavailable>
        ) : !vulns.available ? (
          <Unavailable>
            {!vulns.trivy_installed
              ? "No scanner on this machine yet, so nothing has been checked. That is unknown, not clean."
              : vulns.host_scan_error
                ? `The scanner ran but the host scan failed: ${vulns.host_scan_error}. That is unknown, not clean.`
                : "CVE scan still pending — the Base has not finished its first scan yet. That is unknown, not clean."}
          </Unavailable>
        ) : (
          <>
            <VulnTarget
              label="Host OS"
              summary={vulns.host}
              defaultOpen
              fixHint={
                (vulns.host.fixable_critical ?? 0) + (vulns.host.fixable_high ?? 0) > 0
                  ? reboot_required
                    ? `Reboot to finish applying patches — ownbasectl security reboot ${base.name} --wait`
                    : `Apply host patches with ownbasectl security fix ${base.name} --reboot`
                  : undefined
              }
            />
            {vulns.images?.map((image) =>
              image.scan_failed ? (
                <Row key={image.service} label={image.service} title={image.image}>
                  <span className="text-warn-fg">
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
            <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
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
              <span className="text-bad-fg">{drift_count ?? 0}</span>
            </Row>
            {drift_files && (
              <p className="selectable mt-2 font-mono text-xs leading-relaxed text-fg-muted">
                {drift_files.join("\n")}
              </p>
            )}
            <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
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
  if (total === 0) return <span className="text-good-fg">none found</span>;
  const fixable =
    (summary.fixable_critical ?? 0) + (summary.fixable_high ?? 0);
  const unfixed =
    summary.critical + summary.high - fixable;
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      {summary.critical > 0 && <Badge tone="bad">{summary.critical} critical</Badge>}
      {summary.high > 0 && <Badge tone="warn">{summary.high} high</Badge>}
      <span className="text-xs text-fg-subtle">
        {summary.medium + summary.low} lower
        {summary.critical + summary.high > 0 && (
          <>
            {" · "}
            {fixable > 0 ? (
              <span className="text-warn-fg">{fixable} with a patch</span>
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
    <div className="border-b border-line py-2 last:border-b-0">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <button
          type="button"
          className={cx(
            "flex min-w-0 items-center gap-2 text-left text-sm text-fg",
            hasTop && "cursor-pointer hover:text-fg",
          )}
          onClick={() => hasTop && setOpen((v) => !v)}
          disabled={!hasTop}
          title={title}
        >
          {hasTop && (
            <span className="font-mono text-xs text-fg-subtle">{open ? "▾" : "▸"}</span>
          )}
          <span className="truncate">{label}</span>
        </button>
        <Severities summary={summary} />
      </div>
      {open && hasTop && (
        <div className="mt-2">
          <VulnList findings={top} />
          {top.length >= 20 && (
            <p className="mt-1 text-xs text-fg-subtle">
              Showing the 20 most severe. Medium and low are counted above but not listed.
            </p>
          )}
          {fixHint && (
            <p className="mt-2 text-xs text-fg-subtle">{fixHint}</p>
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
    <ul className="divide-y divide-line border-t border-line">
      {findings.map((finding) => (
        <li key={`${finding.vuln_id}-${finding.package}`} className="py-2 text-xs">
          <div className="flex items-center justify-between gap-3">
            <span className="selectable font-mono text-fg-muted">{finding.vuln_id}</span>
            <Badge tone={finding.severity.toUpperCase() === "CRITICAL" ? "bad" : "warn"}>
              {finding.severity.toLowerCase()}
            </Badge>
          </div>
          <p className="mt-0.5 text-fg-subtle">
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

function RescanAction({
  base,
  onChanged,
  scanning,
}: {
  base: string;
  onChanged: () => void;
  scanning?: boolean;
}) {
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
      <Button variant="secondary" busy={busy || !!scanning} disabled={!!scanning} onClick={() => void rescan()}>
        {scanning ? "Scanning…" : "Rescan"}
      </Button>
      {msg && <p className="max-w-xs text-right text-xs text-fg-subtle">{msg}</p>}
    </div>
  );
}

function RebootAction({ base, onChanged }: { base: string; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [lines, setLines] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [phase, setPhase] = useState<"idle" | "rebooting" | "back">("idle");

  function reboot() {
    if (
      !window.confirm(
        "Every service on this Base will stop and restart with the machine. The outage is typically 30–60 seconds. A CVE rescan runs automatically on boot. Reboot now?",
      )
    ) {
      return;
    }
    setBusy(true);
    setPhase("rebooting");
    setError(null);
    setLines([]);
    const collected: string[] = [];
    // --wait blocks until the API answers again after the reboot.
    const stream = api.securityReboot(
      base,
      (event: StreamEvent) => {
        if (event.kind === "stdout" || event.kind === "stderr") {
          collected.push(event.line);
          setLines([...collected]);
        }
      },
      { wait: true },
    );
    stream.done
      .then((code) => {
        setBusy(false);
        if (code !== 0) {
          setError("The reboot command did not finish cleanly.");
          setPhase("idle");
        } else {
          setPhase("back");
        }
        onChanged();
      })
      .catch((err: unknown) => {
        setBusy(false);
        setPhase("idle");
        setError(err instanceof Error ? err.message : String(err));
      });
  }

  return (
    <div className="flex flex-col items-end gap-2">
      <Button variant="danger" busy={busy} onClick={reboot}>
        {phase === "rebooting" ? "Waiting for Base…" : phase === "back" ? "Back" : "Reboot now"}
      </Button>
      {error && <p className="max-w-xs text-right text-xs text-bad-fg">{error}</p>}
      {phase === "back" && (
        <p className="max-w-xs text-right text-xs text-fg-muted">
          Base is back. A CVE rescan is running — counts refresh in a few minutes.
        </p>
      )}
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
  const hasConfig = Boolean(status.config?.repo_url);

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
            <p className="text-sm leading-relaxed text-fg-muted">
              No snapshot has ever been taken, so there is no way back from a lost
              disk. Backups go to an encrypted off-machine repository you own
              (S3, B2, or SFTP).
            </p>
            {hasConfig ? (
              <BackupSetupForm base={base.name} onDone={onChanged} />
            ) : (
              <Unavailable>
                Set up a config repo first (Overview) — backup settings are
                committed to ownbase.yaml.
              </Unavailable>
            )}
          </div>
        ) : (
          <>
            <Row label="Last snapshot" title={absolute(last_backup)}>
              {ago(last_backup)}
            </Row>
            <Row label="Provably restorable">
              {backup_restorable ? (
                <span className="text-good-fg">yes</span>
              ) : (
                <span className="text-warn-fg">not yet verified</span>
              )}
            </Row>
            <Row label="Last restore drill" title={absolute(last_verified)}>
              {ago(last_verified)}
            </Row>
            <p className="mt-3 text-xs leading-relaxed text-fg-subtle">
              The drill is the part that matters: the Base restores its newest
              snapshot into an isolated directory, checks it, and when Postgres is in
              the backup starts a real database from it and waits for recovery. Until
              that has passed, &quot;restorable&quot; is an assumption.
            </p>
          </>
        )}
      </Panel>

      {configured && (
        <>
          <BackupLifecycle base={base.name} />
          <DBRecovery base={base.name} />
        </>
      )}
    </div>
  );
}

function BackupLifecycle({ base }: { base: string }) {
  const [busy, setBusy] = useState<"prune" | "rekey" | "kit" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lines, setLines] = useState<string[] | null>(null);
  const [generatedPw, setGeneratedPw] = useState<string | null>(null);
  const [kit, setKit] = useState<import("../lib/types").RecoveryKit | null>(null);
  const [showKit, setShowKit] = useState(false);

  async function prune() {
    if (
      !window.confirm(
        "Run restic forget+prune on this Base's backup repository?\n\nUnder append-only mode this is the only way old snapshots are removed.",
      )
    ) {
      return;
    }
    setBusy("prune");
    setError(null);
    setLines(null);
    try {
      const out = await api.backupPrune(base);
      setLines(out.trim().split("\n").filter(Boolean));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function rekey() {
    if (
      !window.confirm(
        "Rotate the restic repository password?\n\nA new password will be generated. Save it immediately — it is a root recovery secret.",
      )
    ) {
      return;
    }
    setBusy("rekey");
    setError(null);
    setGeneratedPw(null);
    setLines(null);
    try {
      const r = await api.backupRekey(base, { generate: true });
      if (r.generated_password) setGeneratedPw(r.generated_password);
      setLines(["Rekey completed."]);
    } catch (err) {
      const e = err as Error & { generated_password?: string };
      if (e.generated_password) setGeneratedPw(e.generated_password);
      setError(e.message || String(err));
    } finally {
      setBusy(null);
    }
  }

  async function loadKit() {
    if (showKit) {
      setShowKit(false);
      setKit(null);
      return;
    }
    setBusy("kit");
    setError(null);
    try {
      setKit(await api.backupRecoveryKit(base));
      setShowKit(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Panel
      title="Backup lifecycle"
      subtitle="Prune, rotate the restic password, or reprint the recovery kit from the vault."
    >
      <div className="flex flex-wrap gap-2">
        <Button busy={busy === "prune"} disabled={busy !== null} onClick={() => void prune()}>
          Prune snapshots
        </Button>
        <Button busy={busy === "rekey"} disabled={busy !== null} onClick={() => void rekey()}>
          Rekey (generate password)
        </Button>
        <Button
          variant="secondary"
          busy={busy === "kit"}
          disabled={busy !== null}
          onClick={() => void loadKit()}
        >
          {showKit ? "Hide recovery kit" : "Show recovery kit"}
        </Button>
      </div>
      {generatedPw && (
        <div className="mt-3 space-y-2 rounded-md border border-warn-line bg-warn-soft p-3">
          <p className="text-xs font-medium text-warn-fg">
            Save this restic password now — it is not recoverable from OwnBase later.
          </p>
          <p className="selectable break-all font-mono text-sm text-fg">{generatedPw}</p>
          <CopyButton value={generatedPw} label="Copy password" />
        </div>
      )}
      {showKit && kit && (
        <div className="mt-3 space-y-2 rounded-md border border-line bg-surface-sunken p-3 text-xs">
          <Row label="Repo">
            <span className="selectable font-mono">{kit.repo}</span>
          </Row>
          <Row label="Password">
            <span className="selectable font-mono">{kit.password}</span>
          </Row>
          {kit.cloud_env_vars && kit.cloud_env_vars.length > 0 && (
            <Row label="Cloud env">{kit.cloud_env_vars.join(", ")}</Row>
          )}
          <div className="flex gap-2 pt-1">
            <CopyButton value={kit.password} label="Copy password" />
            <CopyButton
              value={[kit.repo, kit.password, kit.restic_command]
                .filter(Boolean)
                .join("\n")}
              label="Copy kit"
            />
          </div>
          {kit.note && <p className="text-fg-subtle">{kit.note}</p>}
        </div>
      )}
      {error && <p className="mt-2 text-xs text-bad-fg">{error}</p>}
      {lines && <LogView lines={lines} className="mt-3 max-h-40 w-full" />}
    </Panel>
  );
}

function DBRecovery({ base }: { base: string }) {
  const load = useCallback(() => api.dbStatus(base), [base]);
  const state = useAsync(load);
  const [to, setTo] = useState("");
  const [into, setInto] = useState<"scratch" | "production">("scratch");
  const [confirmName, setConfirmName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  async function runRestore() {
    if (into === "production") {
      if (confirmName.trim() !== base) return;
      if (
        !window.confirm(
          `This replaces the live database on ${base}. Type-confirm is done. Proceed?`,
        )
      ) {
        return;
      }
    } else if (
      !window.confirm(
        "Restore Postgres into a scratch instance on the Base (production keeps serving)?",
      )
    ) {
      return;
    }
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const out = await api.dbRestore(base, {
        to: to.trim() || undefined,
        into,
      });
      setResult(
        out.scratch_endpoint
          ? `Scratch restore ready at ${out.scratch_endpoint}`
          : `Restore into ${out.into} finished.`,
      );
      state.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const s = state.data;

  return (
    <Panel
      title="Postgres recovery"
      subtitle="Point-in-time restore from pgBackRest. Defaults to a scratch instance."
      action={
        <Button variant="secondary" busy={state.refreshing} onClick={state.reload}>
          Refresh window
        </Button>
      }
    >
      {state.loading ? (
        <Spinner />
      ) : state.error ? (
        <p className="text-xs text-fg-subtle">
          No Postgres recovery window available ({state.error}).
        </p>
      ) : s ? (
        <div className="mb-4 space-y-1">
          {s.postgres_version && (
            <Row label="Postgres">{s.postgres_version}</Row>
          )}
          <Row label="Earliest" title={absolute(s.earliest_recovery)}>
            {ago(s.earliest_recovery, "—")}
          </Row>
          <Row label="Latest" title={absolute(s.latest_recovery)}>
            {ago(s.latest_recovery, "—")}
          </Row>
          {s.backups && (
            <Row label="Backups held">{s.backups.length}</Row>
          )}
        </div>
      ) : null}

      <div className="space-y-3 border-t border-line pt-3">
        <Field
          label="Recover to"
          hint='UTC timestamp, e.g. 2026-07-25 14:00:00+00. Empty = everything the repository holds.'
        >
          <Input
            value={to}
            onChange={(e) => setTo(e.target.value)}
            placeholder="(latest)"
            spellCheck={false}
          />
        </Field>
        <div className="flex flex-wrap gap-4 text-sm text-fg-muted">
          <label className="flex items-center gap-2">
            <input
              type="radio"
              checked={into === "scratch"}
              onChange={() => setInto("scratch")}
            />
            Scratch (safe default)
          </label>
          <label className="flex items-center gap-2">
            <input
              type="radio"
              checked={into === "production"}
              onChange={() => setInto("production")}
            />
            Production (replaces live DB)
          </label>
        </div>
        {into === "production" && (
          <Field label={`Type ${base} to confirm production restore`}>
            <Input
              value={confirmName}
              onChange={(e) => setConfirmName(e.target.value)}
              spellCheck={false}
            />
          </Field>
        )}
        <Button
          variant={into === "production" ? "danger" : "primary"}
          busy={busy}
          disabled={into === "production" && confirmName.trim() !== base}
          onClick={() => void runRestore()}
        >
          {into === "production" ? "Restore over production" : "Restore to scratch"}
        </Button>
        {error && <p className="text-xs text-bad-fg">{error}</p>}
        {result && <p className="text-xs text-good-fg">{result}</p>}
      </div>
    </Panel>
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
        <p className="text-xs text-fg-subtle">
          This takes minutes — it is a real restore.
        </p>
      )}
      {error && (
        <p className="max-w-md text-right text-xs leading-relaxed text-bad-fg">
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

function driftStatusLine(d: ServiceDrift): string {
  if (d.up_to_date) return "up to date";
  if (d.commits_behind > 0) {
    const behind = `${d.commits_behind} commit${d.commits_behind === 1 ? "" : "s"} behind ${d.branch || "the default branch"}`;
    return d.newest_tag ? `${behind} · newest tag ${d.newest_tag}` : behind;
  }
  if (d.newest_tag) return `newer tag available (${d.newest_tag})`;
  return "behind its source repo";
}

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
    <div className="space-y-5">
      <CoreUpgrade base={base.name} onChanged={onChanged} />
      <Panel
        title="Service updates"
        subtitle="How far each service is from its source repo. Updating commits a pin to your config repo after you confirm the diff."
      >
        {drift.length === 0 ? (
          <Unavailable>
            Nothing to compare yet. The Base checks each service&apos;s source repo on
            its own schedule and reports what it finds here.
          </Unavailable>
        ) : (
          <ul className="divide-y divide-line">
            {drift.map((d) => (
              <li key={d.service} className="py-3 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span className="flex items-center gap-2 text-sm text-fg">
                    <Dot tone={d.up_to_date ? "good" : "warn"} />
                    {d.service}
                  </span>
                  <span className="flex items-center gap-3">
                    <span className="font-mono text-xs text-fg-subtle" title={d.ref}>
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
                <p className="mt-1 pl-4 text-xs text-fg-subtle">
                  {driftStatusLine(d)}
                </p>
                {openFor === d.service && (
                  <div className="mt-3 pl-4">
                    <DeployForm
                      base={base.name}
                      service={d.service}
                      suggestedRef={pickDeployRef(
                        d.commits_behind,
                        d.branch,
                        d.newest_tag,
                      )}
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
    </div>
  );
}

function CoreUpgrade({ base, onChanged }: { base: string; onChanged: () => void }) {
  const [check, setCheck] = useState<import("../lib/types").UpgradeCheck | null>(null);
  const [busy, setBusy] = useState<"check" | "apply" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lines, setLines] = useState<string[] | null>(null);

  async function doCheck() {
    setBusy("check");
    setError(null);
    try {
      setCheck(await api.upgradeCheck(base));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  function doApply() {
    if (
      !window.confirm(
        "Pull the latest pinned Caddy image and restart the reverse proxy briefly?",
      )
    ) {
      return;
    }
    setBusy("apply");
    setError(null);
    setLines([]);
    const collected: string[] = [];
    const stream = api.upgradeApply(base, (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        collected.push(event.line);
        setLines([...collected]);
      }
    });
    stream.done
      .then((code) => {
        setBusy(null);
        if (code !== 0) setError("Upgrade did not finish cleanly.");
        else void doCheck();
        onChanged();
      })
      .catch((err: unknown) => {
        setBusy(null);
        setError(err instanceof Error ? err.message : String(err));
      });
  }

  return (
    <Panel
      title="OwnBase core (Caddy)"
      subtitle="Managed outside ownbase.yaml. Check status, then apply to pull and restart."
      action={
        <div className="flex gap-2">
          <Button
            variant="secondary"
            busy={busy === "check"}
            disabled={busy !== null}
            onClick={() => void doCheck()}
          >
            Check
          </Button>
          <Button busy={busy === "apply"} disabled={busy !== null} onClick={doApply}>
            Apply upgrade
          </Button>
        </div>
      }
    >
      {check ? (
        <ul className="divide-y divide-line text-xs">
          {check.packages.map((pkg) => (
            <li
              key={pkg.name}
              className="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <span className="font-mono text-fg-muted">{pkg.name}</span>
              <span className="text-fg-subtle">
                {pkg.running ? "running" : "stopped"} · {pkg.image}
                {pkg.digest ? `@${pkg.digest.slice(0, 12)}` : " (no digest pinned)"}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-fg-subtle">Not checked yet this session.</p>
      )}
      {error && <p className="mt-2 text-xs text-bad-fg">{error}</p>}
      {lines && <LogView lines={lines} className="mt-3 max-h-40 w-full" />}
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
        <ul className="divide-y divide-line">
          {actions.map((action, i) => (
            <li
              key={`${action.time}-${i}`}
              className="flex flex-wrap items-center justify-between gap-3 py-2.5 text-xs first:pt-0 last:pb-0"
            >
              <span className="flex min-w-0 items-center gap-2">
                <Dot tone={action.outcome === "applied" ? "good" : "bad"} />
                <span className="font-mono text-fg-muted">{action.action}</span>
                {action.target && (
                  <span className="selectable truncate text-fg-subtle">
                    {action.target}
                  </span>
                )}
              </span>
              <span className="text-fg-subtle" title={absolute(action.time)}>
                {ago(action.time)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {status.audit.total_seen > actions.length && (
        <p className="mt-3 text-xs text-fg-subtle">
          Showing the most recent {actions.length} of {status.audit.total_seen}.
        </p>
      )}
    </Panel>
  );
}