// The small set of primitives every screen is built from. Deliberately plain:
// the app's job is to make dense status information legible, and a component
// library would mostly get in the way of that.

import type { ReactNode } from "react";
import { forwardRef, useEffect, useRef } from "react";

import { cx } from "../lib/cx";

// ---------------------------------------------------------------------------
// Button
// ---------------------------------------------------------------------------

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  busy?: boolean;
}

const buttonVariants: Record<ButtonVariant, string> = {
  primary:
    "bg-emerald-500 text-emerald-950 hover:bg-emerald-400 disabled:bg-emerald-500/40 disabled:text-emerald-950/50",
  secondary:
    "bg-zinc-800 text-zinc-100 hover:bg-zinc-700 border border-zinc-700 disabled:text-zinc-500",
  ghost: "text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100 disabled:text-zinc-600",
  danger:
    "bg-red-500/10 text-red-300 border border-red-500/30 hover:bg-red-500/20 disabled:text-red-300/40",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", busy, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || busy}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-lg px-3.5 py-2",
        "text-sm font-medium transition-colors disabled:cursor-not-allowed",
        buttonVariants[variant],
        className,
      )}
      {...rest}
    >
      {busy && <Spinner />}
      {children}
    </button>
  );
});

export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      className={cx("h-3.5 w-3.5 animate-spin", className)}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden
    >
      <circle
        cx="12"
        cy="12"
        r="9"
        stroke="currentColor"
        strokeWidth="3"
        strokeOpacity="0.25"
      />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  );
}

// ---------------------------------------------------------------------------
// Form fields
// ---------------------------------------------------------------------------

interface FieldProps {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  children: ReactNode;
}

export function Field({ label, hint, error, children }: FieldProps) {
  return (
    <label className="block space-y-1.5">
      <span className="text-xs font-medium uppercase tracking-wide text-zinc-400">
        {label}
      </span>
      {children}
      {error ? (
        <span className="block text-sm text-red-400">{error}</span>
      ) : hint ? (
        <span className="block text-sm leading-snug text-zinc-500">{hint}</span>
      ) : null}
    </label>
  );
}

export const Input = forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...rest }, ref) {
    return (
      <input
        ref={ref}
        className={cx(
          "w-full rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2",
          "text-sm text-zinc-100 placeholder:text-zinc-600",
          "focus:border-emerald-500/60 focus:outline-none focus:ring-1 focus:ring-emerald-500/40",
          className,
        )}
        {...rest}
      />
    );
  },
);

// ---------------------------------------------------------------------------
// Surfaces and status
// ---------------------------------------------------------------------------

export function Card({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cx(
        "rounded-xl border border-zinc-800 bg-zinc-900/60 p-5",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** Health colour. `unknown` exists so "we have not checked" never reads as "fine". */
export type Tone = "good" | "warn" | "bad" | "unknown" | "info";

const toneDot: Record<Tone, string> = {
  good: "bg-emerald-400",
  warn: "bg-amber-400",
  bad: "bg-red-400",
  unknown: "bg-zinc-600",
  info: "bg-sky-400",
};

export function Dot({ tone, className }: { tone: Tone; className?: string }) {
  return (
    <span
      className={cx("inline-block h-2 w-2 shrink-0 rounded-full", toneDot[tone], className)}
      aria-hidden
    />
  );
}

const badgeTone: Record<Tone, string> = {
  good: "bg-emerald-500/10 text-emerald-300 border-emerald-500/25",
  warn: "bg-amber-500/10 text-amber-300 border-amber-500/25",
  bad: "bg-red-500/10 text-red-300 border-red-500/25",
  unknown: "bg-zinc-700/30 text-zinc-400 border-zinc-700",
  info: "bg-sky-500/10 text-sky-300 border-sky-500/25",
};

export function Badge({
  tone = "unknown",
  children,
  className,
}: {
  tone?: Tone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5",
        "text-xs font-medium",
        badgeTone[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** An error worth showing in place, with the CLI's own words. */
export function ErrorNote({
  title,
  detail,
  onRetry,
}: {
  title: string;
  detail?: string | null;
  onRetry?: () => void;
}) {
  return (
    <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-4">
      <p className="text-sm font-medium text-red-300">{title}</p>
      {detail && (
        <pre className="selectable mt-2 whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-red-200/80">
          {detail}
        </pre>
      )}
      {onRetry && (
        <Button variant="secondary" className="mt-3" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  );
}

export function EmptyState({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="rounded-xl border border-dashed border-zinc-800 p-10 text-center">
      <p className="text-sm font-medium text-zinc-300">{title}</p>
      {children && (
        <div className="mx-auto mt-2 max-w-md text-sm leading-relaxed text-zinc-500">
          {children}
        </div>
      )}
    </div>
  );
}

/** A command the user can copy and run in a terminal. */
export function CommandLine({ children }: { children: ReactNode }) {
  return (
    // A command with a long path in it must not be able to widen its container:
    // one absolute path was enough to give the whole window a horizontal
    // scrollbar and squeeze every panel beside it. `anywhere` rather than
    // `break-all` because it breaks at spaces and slashes first and only splits a
    // word when there is no other option, so `ownbasectl` stays readable.
    <code className="selectable rounded-md bg-zinc-800/80 px-1.5 py-0.5 font-mono text-[0.8125rem] text-zinc-200 [overflow-wrap:anywhere]">
      {children}
    </code>
  );
}

// ---------------------------------------------------------------------------
// Page structure
// ---------------------------------------------------------------------------

/** A titled block of related facts. */
export function Panel({
  title,
  subtitle,
  action,
  children,
  className,
}: {
  title: string;
  subtitle?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("rounded-xl border border-zinc-800 bg-zinc-900/40", className)}>
      <header className="flex items-start justify-between gap-4 border-b border-zinc-800 px-5 py-3.5">
        <div>
          <h2 className="text-sm font-medium text-zinc-200">{title}</h2>
          {subtitle && (
            <p className="mt-0.5 text-xs leading-relaxed text-zinc-500">{subtitle}</p>
          )}
        </div>
        {action && <div className="shrink-0">{action}</div>}
      </header>
      <div className="px-5 py-4">{children}</div>
    </section>
  );
}

/** One labelled fact. Values are selectable — they get pasted into terminals. */
export function Row({
  label,
  children,
  title,
}: {
  label: string;
  children: ReactNode;
  title?: string;
}) {
  return (
    <div className="flex items-baseline justify-between gap-6 py-1.5">
      <span className="shrink-0 text-sm text-zinc-500">{label}</span>
      <span
        className="selectable min-w-0 break-words text-right text-sm text-zinc-200"
        title={title}
      >
        {children}
      </span>
    </div>
  );
}

interface TabDef<T extends string> {
  id: T;
  label: string;
  /** Shown as a count beside the label, e.g. the number of findings. */
  badge?: number;
  badgeTone?: Tone;
}

export function Tabs<T extends string>({
  tabs,
  active,
  onChange,
}: {
  tabs: Array<TabDef<T>>;
  active: T;
  onChange: (id: T) => void;
}) {
  return (
    <div role="tablist" className="flex gap-1 border-b border-zinc-800">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          role="tab"
          aria-selected={tab.id === active}
          onClick={() => onChange(tab.id)}
          className={cx(
            "-mb-px flex items-center gap-2 border-b-2 px-3 py-2 text-sm transition-colors",
            tab.id === active
              ? "border-emerald-500 text-zinc-100"
              : "border-transparent text-zinc-500 hover:text-zinc-300",
          )}
        >
          {tab.label}
          {tab.badge !== undefined && tab.badge > 0 && (
            <Badge tone={tab.badgeTone ?? "unknown"}>{tab.badge}</Badge>
          )}
        </button>
      ))}
    </div>
  );
}

/**
 * Live output from a running command.
 *
 * Follows the tail only while the user is already at the bottom. Yanking the
 * view back down while someone is reading an error further up is the one thing
 * a log pane must not do.
 */
export function LogView({
  lines,
  className,
}: {
  lines: string[];
  className?: string;
}) {
  const ref = useRef<HTMLPreElement>(null);
  const pinned = useRef(true);

  useEffect(() => {
    const el = ref.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  function onScroll() {
    const el = ref.current;
    if (!el) return;
    pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  }

  return (
    <pre
      ref={ref}
      onScroll={onScroll}
      className={cx(
        "selectable overflow-auto whitespace-pre-wrap break-words rounded-lg",
        "border border-zinc-800 bg-zinc-950 p-3 font-mono text-xs leading-relaxed text-zinc-400",
        className,
      )}
    >
      {lines.length === 0 ? "Waiting for output…" : lines.join("\n")}
    </pre>
  );
}

/** The "we asked but the Base has not answered yet" state, used a lot here. */
export function Unavailable({ children }: { children: ReactNode }) {
  return <p className="text-sm leading-relaxed text-zinc-500">{children}</p>;
}
