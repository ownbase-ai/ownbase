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
      <p className="text-xs text-fg-muted">
        Commit: <span className="font-mono text-fg-muted">{commitMessage}</span>
      </p>
      <pre className="selectable max-h-56 overflow-auto rounded-md border border-line bg-surface-sunken p-3 font-mono text-[11px] leading-relaxed text-fg-muted">
        {diff || "(no textual diff)"}
      </pre>
    </div>
  );
}
