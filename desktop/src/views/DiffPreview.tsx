/** Shared YAML-diff + commit message block for config-repo previews. */
export function DiffPreview({
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
