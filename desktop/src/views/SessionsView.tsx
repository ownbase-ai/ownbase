import { useCallback, useState } from "react";

import { CastPlayer } from "../components/CastPlayer";
import { CopyButton } from "../components/CopyButton";
import {
  Badge,
  Button,
  CommandLine,
  Dot,
  EmptyState,
  ErrorNote,
  Panel,
  Row,
  Spinner,
} from "../components/ui";
import type { Tone } from "../components/ui";
import * as api from "../lib/api";
import { cx } from "../lib/cx";
import { absolute, ago, bytes, duration } from "../lib/format";
import type { SessionMeta } from "../lib/types";
import { useAsync } from "../lib/useAsync";

/**
 * The audit trail.
 *
 * This is the feature that makes it reasonable to let an agent have root on a
 * machine you own. Reading a Base's logs tells you what the machine noticed; a
 * recording tells you what was actually typed and what came back, including the
 * commands that left no trace anywhere else. Every shell opened through
 * `ownbasectl ssh` is here, and recording cannot be turned off.
 */
export function SessionsView({ baseFilter }: { baseFilter?: string }) {
  const load = useCallback(() => api.listSessions(baseFilter), [baseFilter]);
  const sessions = useAsync<SessionMeta[]>(load);
  const [selected, setSelected] = useState<string | null>(null);

  const list = sessions.data ?? [];
  const current = list.find((s) => s.id === selected) ?? null;

  return (
    <div className="flex h-full min-h-0">
      <div className="flex w-80 shrink-0 flex-col border-r border-line">
        <header className="flex items-center justify-between gap-3 px-5 pb-3 pt-8">
          <div>
            <h1 className="text-base font-medium text-fg">Sessions</h1>
            <p className="mt-0.5 text-xs text-fg-subtle">
              {baseFilter ? baseFilter : "Every Base"}, newest first
            </p>
          </div>
          <Button variant="ghost" busy={sessions.refreshing} onClick={sessions.reload}>
            Refresh
          </Button>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
          {sessions.loading ? (
            <div className="px-3 py-4">
              <Spinner className="text-fg-faint" />
            </div>
          ) : list.length === 0 ? (
            <p className="px-3 py-4 text-sm leading-relaxed text-fg-subtle">
              No sessions recorded yet. Open one with{" "}
              <CommandLine>ownbasectl ssh {baseFilter ?? "<name>"}</CommandLine>.
            </p>
          ) : (
            <ul className="space-y-0.5">
              {list.map((session) => (
                <li key={session.id}>
                  <SessionButton
                    session={session}
                    active={session.id === selected}
                    onClick={() => setSelected(session.id)}
                  />
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {sessions.error ? (
          <div className="p-8">
            <ErrorNote
              title="Could not read the session index"
              detail={sessions.error}
              onRetry={sessions.reload}
            />
          </div>
        ) : current ? (
          <SessionDetail session={current} />
        ) : (
          <div className="p-8">
            <EmptyState title="Pick a session">
              <p>
                Every shell OwnBase opened on a Base is recorded in{" "}
                <a
                  className="text-fg-muted underline decoration-line-strong underline-offset-2"
                  href="https://docs.asciinema.org/manual/asciicast/v2/"
                  target="_blank"
                  rel="noreferrer"
                >
                  asciicast v2
                </a>
                , input included. They are plain files you own, replayable with{" "}
                <code className="font-mono text-xs">asciinema play</code> whether or
                not OwnBase is installed.
              </p>
            </EmptyState>
          </div>
        )}
      </div>
    </div>
  );
}

function SessionButton({
  session,
  active,
  onClick,
}: {
  session: SessionMeta;
  active: boolean;
  onClick: () => void;
}) {
  const tone = outcomeTone(session);
  return (
    <button
      onClick={onClick}
      className={cx(
        "w-full rounded-lg px-3 py-2.5 text-left transition-colors",
        active ? "bg-surface-sunken" : "hover:bg-surface",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-2">
          <Dot tone={tone} />
          <span className="truncate text-sm text-fg">
            {session.command || "interactive shell"}
          </span>
        </span>
        <span className="shrink-0 text-xs text-fg-subtle" title={absolute(session.started_at)}>
          {ago(session.started_at)}
        </span>
      </div>
      <p className="mt-0.5 pl-4 text-xs text-fg-subtle">
        {session.base}
        {session.invoker && session.invoker !== "cli" ? ` · by ${session.invoker}` : ""}
      </p>
    </button>
  );
}

function outcomeTone(session: SessionMeta): Tone {
  if (!session.ended_at) return "info";
  if (session.error) return "bad";
  if (session.exit_code < 0) return "unknown";
  return session.exit_code === 0 ? "good" : "warn";
}

function outcomeLabel(session: SessionMeta): string {
  if (!session.ended_at) return "still open";
  if (session.error) return "failed";
  if (session.exit_code < 0) return "ended without a status";
  return session.exit_code === 0 ? "exit 0" : `exit ${session.exit_code}`;
}

// ---------------------------------------------------------------------------
// One session
// ---------------------------------------------------------------------------

function SessionDetail({ session }: { session: SessionMeta }) {
  const [mode, setMode] = useState<"replay" | "transcript">("replay");
  const load = useCallback(
    () =>
      mode === "replay"
        ? api.sessionCast(session.id)
        : api.sessionTranscript(session.id),
    [session.id, mode],
  );
  const content = useAsync<string>(load);

  return (
    <div className="space-y-5 p-8">
      <header>
        <div className="flex flex-wrap items-center gap-3">
          <h2 className="text-base font-medium text-fg">
            {session.command || "Interactive shell"}
          </h2>
          <Badge tone={outcomeTone(session)}>{outcomeLabel(session)}</Badge>
        </div>
        <p className="selectable mt-1 font-mono text-xs text-fg-subtle">{session.id}</p>
      </header>

      {/* One column. This pane is already narrowed by the sidebar and the session
          list, and two panels here left a file path in a 280px column. */}
      <Panel
        title="What happened"
        action={<CopyButton value={session.cast_path} label="Copy path" />}
      >
        <Row label="Base">{session.base}</Row>
        <Row label="Logged in as">
          <span className="whitespace-nowrap">
            {session.user}@{session.host}
          </span>
        </Row>
        <Row label="Opened by">{session.invoker || "cli"}</Row>
        <Row label="Started" title={absolute(session.started_at)}>
          {ago(session.started_at)}
        </Row>
        <Row label="Lasted">{duration(session.started_at, session.ended_at)}</Row>
        <Row label="Recorded">
          {bytes(session.bytes)}
          {session.interactive ? " · with a terminal" : " · no terminal"}
        </Row>
        {session.error && (
          <Row label="Error">
            <span className="text-bad-fg">{session.error}</span>
          </Row>
        )}
        <Row label="File">
          <span className="font-mono text-xs">{session.cast_path}</span>
        </Row>
        <p className="mt-3 border-t border-line pt-3 text-xs leading-relaxed text-fg-subtle">
          Replay it outside OwnBase with <CommandLine>asciinema play</CommandLine> and
          that path — it is asciicast v2, an open format, and the file is yours. Mode
          600, because a session can contain anything typed at a prompt.
        </p>
      </Panel>

      <div className="flex items-center gap-2">
        <Button
          variant={mode === "replay" ? "secondary" : "ghost"}
          onClick={() => setMode("replay")}
        >
          Replay
        </Button>
        <Button
          variant={mode === "transcript" ? "secondary" : "ghost"}
          onClick={() => setMode("transcript")}
        >
          Transcript
        </Button>
      </div>

      {content.loading ? (
        <div className="flex items-center gap-3 text-sm text-fg-subtle">
          <Spinner /> Reading the recording…
        </div>
      ) : content.error || content.data === null ? (
        <ErrorNote
          title="Could not read the recording"
          detail={content.error}
          onRetry={content.reload}
        />
      ) : mode === "replay" ? (
        <CastPlayer id={session.id} cast={content.data} />
      ) : (
        <pre className="selectable max-h-[32rem] overflow-auto whitespace-pre-wrap break-words rounded-lg border border-line bg-canvas p-4 font-mono text-xs leading-relaxed text-fg-muted">
          {content.data.trim() || "The session produced no output."}
        </pre>
      )}
    </div>
  );
}
