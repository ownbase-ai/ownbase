import { useEffect, useRef } from "react";
import * as asciinema from "asciinema-player";
import "asciinema-player/dist/bundle/asciinema-player.css";

/**
 * Replays a recorded session.
 *
 * This is asciinema's own player, fed the recording verbatim. That is the point
 * of having chosen asciicast v2 for the format: the app replays these files with
 * the same engine `asciinema play` uses, so nothing about the audit trail
 * depends on OwnBase being installed — or on this app rendering it correctly.
 */
export function CastPlayer({
  cast,
  /** Used only to force a fresh player when the recording changes. */
  id,
}: {
  cast: string;
  id: string;
}) {
  const host = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;

    const player = asciinema.create(
      // `data` rather than a URL: the recording came through the CLI, and the
      // webview has no filesystem access to fetch it with.
      { data: cast, parser: "asciicast" },
      el,
      {
        fit: "width",
        // A recording of a person typing has long pauses in it. Capping the idle
        // gap keeps a ten-minute session watchable without distorting the parts
        // where things actually happen.
        idleTimeLimit: 2,
        terminalFontFamily: "var(--ownbase-mono)",
        theme: "asciinema",
      },
    );

    return () => player.dispose();
  }, [cast, id]);

  return <div ref={host} className="overflow-hidden rounded-lg bg-black" />;
}
