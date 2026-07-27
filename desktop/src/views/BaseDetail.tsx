import { useCallback, useState } from "react";

import { CopyButton } from "../components/CopyButton";
import {
  Badge,
  Button,
  CommandLine,
  Dot,
  EmptyState,
  ErrorNote,
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
 * Read-only by design. Config changes are commits to a Git repo the user owns,
 * and giving the window a second way to make them would mean two answers to
 * "what is deployed". So this shows, points at the command, and gets out of the
 * way — with two exceptions that change nothing about the desired state: taking
 * a backup now, and running the restore drill.
 */
export function BaseDetail({ base }: { base: BaseSummary }) {
  const [tab, setTab] = useState<Tab>("overview");
  const load = useCallback(() => api.checkup(base.name), [base.name]);
  const state = useAsync<Checkup>(load);

  if (!base.registered || !base.host) {
    return <NotReachableYet base={base} />;
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
          <UnreachableNote base={base} detail={state.error} onRetry={state.reload} />
        ) : (
          <Body
            tab={tab}
            base={base}
            status={status}
            findings={findings}
            onChanged={state.reload}
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
}: {
  tab: Tab;
  base: BaseSummary;
  status: BaseStatus;
  findings: Finding[];
  onChanged: () => void;
}) {
  switch (tab) {
    case "overview":
      return <Overview base={base} status={status} findings={findings} />;
    case "services":
      return <Services status={status} />;
    case "security":
      return <Security base={base} status={status} />;
    case "backups":
      return <Backups base={base} status={status} onChanged={onChanged} />;
    case "updates":
      return <Updates base={base} status={status} />;
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
function NotReachableYet({ base }: { base: BaseSummary }) {
  const unregistered = base.kind === "unregistered-vm";
  return (
    <div className="flex h-full flex-col px-8 py-8">
      <h1 className="text-lg font-medium text-zinc-100">{base.name}</h1>
      <div className="mt-6">
        {unregistered ? (
          <EmptyState title="A local VM with this name, but no Base">
            <p>
              Multipass has a VM called <strong>{base.name}</strong> that OwnBase does
              not know about. Adopt it with{" "}
              <CommandLine>ownbasectl adopt {base.name}</CommandLine>, or delete it
              with <CommandLine>ownbasectl delete {base.name}</CommandLine>.
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
              .
            </p>
          </EmptyState>
        )}
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
}: {
  base: BaseSummary;
  status: BaseStatus;
  findings: Finding[];
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
            : "Each of these comes with the command that addresses it."
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
              <li
                key={finding.summary}
                className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3.5 py-3"
              >
                <span className="text-sm text-amber-100/90">{finding.summary}</span>
                <span className="flex items-center gap-2">
                  <CommandLine>{finding.fix}</CommandLine>
                  <CopyButton value={finding.fix} label="Copy" />
                </span>
              </li>
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
            {base.config_repo_url ? (
              <span className="font-mono text-xs">{base.config_repo_url}</span>
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
    </div>
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

function Security({ base, status }: { base: BaseSummary; status: BaseStatus }) {
  const { exposure, access, vulns, drift_detected, drift_count, drift_files } =
    status.security;

  return (
    <div className="space-y-5">
      <Panel
        title="Network exposure"
        subtitle="What this machine believes is reachable from the internet."
      >
        {!exposure.available ? (
          <Unavailable>
            The scan could not run — it needs <code className="font-mono">ss</code> and{" "}
            <code className="font-mono">ufw</code>, which a Base has and a dev machine
            may not. Treat this as unknown rather than clear.
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
            The monitor could not run — it reads fail2ban and the journal, neither of
            which is present here.
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
            ? `Last scanned ${ago(vulns.scanned_at)}.`
            : "Scanned daily by the Base."
        }
      >
        {!vulns.available ? (
          <Unavailable>
            {vulns.trivy_installed
              ? `The scanner ran but the host scan failed${vulns.host_scan_error ? `: ${vulns.host_scan_error}` : ""}. That is unknown, not clean.`
              : "No scanner on this machine yet, so nothing has been checked. That is unknown, not clean."}
          </Unavailable>
        ) : (
          <>
            <Row label="Host OS">
              <Severities summary={vulns.host} />
            </Row>
            {vulns.images?.map((image) => (
              <Row key={image.service} label={image.service} title={image.image}>
                {image.scan_failed ? (
                  <span className="text-amber-300">
                    scan failed{image.scan_error ? ` — ${image.scan_error}` : ""}
                  </span>
                ) : (
                  <Severities summary={image.summary} />
                )}
              </Row>
            ))}
            {vulns.host.top && vulns.host.top.length > 0 && (
              <ul className="mt-3 divide-y divide-zinc-800 border-t border-zinc-800">
                {vulns.host.top.slice(0, 8).map((finding) => (
                  <li key={finding.vuln_id} className="py-2 text-xs">
                    <div className="flex items-center justify-between gap-3">
                      <span className="selectable font-mono text-zinc-300">
                        {finding.vuln_id}
                      </span>
                      <Badge
                        tone={finding.severity.toUpperCase() === "CRITICAL" ? "bad" : "warn"}
                      >
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
            )}
            <p className="mt-3 text-xs leading-relaxed text-zinc-500">
              Apply the fixes that exist with{" "}
              <CommandLine>ownbasectl security fix {base.name}</CommandLine>.
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
              Any difference is a signal worth understanding — compare against the
              desired state with <CommandLine>ownbasectl plan</CommandLine>.
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
  return (
    <span className="inline-flex items-center gap-2">
      {summary.critical > 0 && <Badge tone="bad">{summary.critical} critical</Badge>}
      {summary.high > 0 && <Badge tone="warn">{summary.high} high</Badge>}
      <span className="text-xs text-zinc-500">
        {summary.medium + summary.low} lower
      </span>
    </span>
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
          <Unavailable>
            No snapshot has ever been taken, so there is no way back from a lost disk.
            Turn backups on with{" "}
            <CommandLine>ownbasectl backup setup {base.name}</CommandLine> — they go
            to an encrypted off-machine repository you own (S3, B2, or SFTP).
          </Unavailable>
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

function Updates({ base, status }: { base: BaseSummary; status: BaseStatus }) {
  const drift = status.updates.drift ?? [];
  // Naming one specific service makes the suggested command copy-pasteable
  // instead of a template the reader has to fill in.
  const stale = drift.find((d) => !d.up_to_date);
  const example = stale
    ? `ownbasectl deploy ${base.name} ${stale.service} --ref ${stale.newest_tag || stale.branch || "main"}`
    : null;

  return (
    <Panel
      title="Service updates"
      subtitle="How far each service is from its source repo. Updating is your call, never automatic."
    >
      {drift.length === 0 ? (
        <Unavailable>
          Nothing to compare yet. The Base checks each service's source repo on its
          own schedule and reports what it finds here.
        </Unavailable>
      ) : (
        <>
          <ul className="divide-y divide-zinc-800">
            {drift.map((d) => (
              <li key={d.service} className="py-3 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span className="flex items-center gap-2 text-sm text-zinc-200">
                    <Dot tone={d.up_to_date ? "good" : "warn"} />
                    {d.service}
                  </span>
                  <span className="font-mono text-xs text-zinc-500" title={d.ref}>
                    @{shortRef(d.ref)}
                  </span>
                </div>
                <p className="mt-1 pl-4 text-xs text-zinc-500">
                  {d.up_to_date
                    ? "up to date"
                    : `${d.commits_behind} commit${d.commits_behind === 1 ? "" : "s"} behind ${d.branch || "the default branch"}`}
                  {d.newest_tag && ` · newest tag ${d.newest_tag}`}
                </p>
              </li>
            ))}
          </ul>
          {example && (
            <p className="mt-4 text-xs leading-relaxed text-zinc-500">
              To move one forward: <CommandLine>{example}</CommandLine>. That resolves
              the ref to a concrete commit and commits it to your config repo, so what
              is deployed stays written down.
            </p>
          )}
        </>
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
                <Dot tone={action.outcome === "success" ? "good" : "bad"} />
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