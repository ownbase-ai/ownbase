import { useCallback, useEffect, useRef, useState } from "react";

import { CopyButton } from "../components/CopyButton";
import {
  Badge,
  Button,
  Card,
  CommandLine,
  ErrorNote,
  Field,
  Input,
  LogView,
} from "../components/ui";
import * as api from "../lib/api";
import type { StreamEvent, StreamHandle } from "../lib/cli";
import { cx } from "../lib/cx";
import type { KeygenResult } from "../lib/types";

/**
 * The setup walkthrough, in the same order as the README.
 *
 * The shape of setting up a Base is fixed by something outside OwnBase: a
 * provider authorizes an SSH key when the machine is created, and only a human
 * with a payment method can create the machine. So this cannot be one button.
 * It is a key, a pause while the user goes to their provider, and then an
 * install that takes minutes and is worth watching.
 */
type Step = "name" | "key" | "server" | "install" | "done";

const steps: Array<{ id: Step; label: string }> = [
  { id: "name", label: "Name" },
  { id: "key", label: "SSH key" },
  { id: "server", label: "Server" },
  { id: "install", label: "Install" },
];

export function SetupWizard({
  existingNames,
  onFinished,
  onCancel,
}: {
  existingNames: string[];
  /** Called with the new Base's name once its daemon reports healthy. */
  onFinished: (base: string) => void;
  onCancel: () => void;
}) {
  const [step, setStep] = useState<Step>("name");
  const [name, setName] = useState("");
  const [key, setKey] = useState<KeygenResult | null>(null);
  const [address, setAddress] = useState("");
  const [sshUser, setSSHUser] = useState("root");
  const [caddyEmail, setCaddyEmail] = useState("");

  return (
    <div className="mx-auto flex h-full w-full max-w-2xl flex-col gap-6 overflow-y-auto px-8 py-10">
      <header>
        <h1 className="text-lg font-medium text-zinc-100">Set up a Base</h1>
        <p className="mt-1 text-sm leading-relaxed text-zinc-500">
          About ten minutes, most of it waiting. One step needs you to visit your
          server provider; the rest happens here.
        </p>
      </header>

      <Progress step={step} />

      {step === "name" && (
        <NameStep
          existingNames={existingNames}
          name={name}
          onName={setName}
          onNext={() => setStep("key")}
          onCancel={onCancel}
        />
      )}

      {step === "key" && (
        <KeyStep
          base={name}
          result={key}
          onResult={setKey}
          onBack={() => setStep("name")}
          onNext={() => setStep("server")}
        />
      )}

      {step === "server" && (
        <ServerStep
          publicKey={key?.public_key ?? ""}
          address={address}
          onAddress={setAddress}
          sshUser={sshUser}
          onSSHUser={setSSHUser}
          caddyEmail={caddyEmail}
          onCaddyEmail={setCaddyEmail}
          onBack={() => setStep("key")}
          onNext={() => setStep("install")}
        />
      )}

      {step === "install" && (
        <InstallStep
          base={name}
          address={address}
          sshUser={sshUser}
          caddyEmail={caddyEmail}
          onBack={() => setStep("server")}
          onDone={() => setStep("done")}
        />
      )}

      {step === "done" && <DoneStep base={name} onOpen={() => onFinished(name)} />}
    </div>
  );
}

function Progress({ step }: { step: Step }) {
  const index = step === "done" ? steps.length : steps.findIndex((s) => s.id === step);
  return (
    <ol className="flex items-center gap-2 text-xs">
      {steps.map((s, i) => (
        <li key={s.id} className="flex items-center gap-2">
          <span
            className={cx(
              "flex items-center gap-1.5 rounded-full px-2.5 py-1",
              i < index && "text-emerald-400",
              i === index && "bg-zinc-800 text-zinc-100",
              i > index && "text-zinc-600",
            )}
          >
            <span className="font-mono">{i < index ? "✓" : i + 1}</span>
            {s.label}
          </span>
          {i < steps.length - 1 && <span className="text-zinc-700">→</span>}
        </li>
      ))}
    </ol>
  );
}

function Footer({ children }: { children: React.ReactNode }) {
  return <div className="mt-6 flex justify-between gap-3">{children}</div>;
}

// ---------------------------------------------------------------------------
// 1. Name
// ---------------------------------------------------------------------------

function NameStep({
  existingNames,
  name,
  onName,
  onNext,
  onCancel,
}: {
  existingNames: string[];
  name: string;
  onName: (value: string) => void;
  onNext: () => void;
  onCancel: () => void;
}) {
  const trimmed = name.trim();
  const taken = existingNames.includes(trimmed);
  const shape = /^[a-z0-9][a-z0-9-]*$/.test(trimmed);
  const error = taken
    ? "You already have a Base with this name."
    : trimmed && !shape
      ? "Lowercase letters, numbers, and hyphens."
      : null;

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Name this Base</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        The name is how you refer to the machine from now on, here and in
        commands like <CommandLine>ownbasectl status {trimmed || "mybase"}</CommandLine>.
        It stays on your computer — the server never learns it.
      </p>

      <form
        className="mt-5"
        onSubmit={(e) => {
          e.preventDefault();
          if (trimmed && !error) onNext();
        }}
      >
        <Field label="Name" error={error} hint="Something short you will type often.">
          <Input
            autoFocus
            value={name}
            onChange={(e) => onName(e.target.value)}
            placeholder="mybase"
            spellCheck={false}
            autoCapitalize="off"
          />
        </Field>

        <Footer>
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!trimmed || Boolean(error)}>
            Continue
          </Button>
        </Footer>
      </form>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 2. Key
// ---------------------------------------------------------------------------

function KeyStep({
  base,
  result,
  onResult,
  onBack,
  onNext,
}: {
  base: string;
  result: KeygenResult | null;
  onResult: (result: KeygenResult) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const generate = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      onResult(await api.keygen(base.trim()));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [base, onResult]);

  // Runs on arrival rather than behind a button: there is no decision to make
  // here, and re-running keygen prints the existing key instead of replacing
  // it, so arriving twice is harmless.
  useEffect(() => {
    if (!result) void generate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Your key for this Base</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        The private half is in your vault and will never be written to disk. When
        something needs to prove it is you, the credential agent signs for it. Each
        Base gets its own key, so retiring one Base revokes exactly one credential.
      </p>

      {error && (
        <div className="mt-4">
          <ErrorNote title="Could not create the key" detail={error} onRetry={generate} />
        </div>
      )}

      {result && (
        <div className="mt-4">
          <div className="flex items-center justify-between gap-3">
            <Badge tone={result.created ? "good" : "info"}>
              {result.created ? "New key created" : "Existing key reused"}
            </Badge>
            <CopyButton value={result.public_key} label="Copy public key" />
          </div>
          <pre className="selectable mt-3 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950 p-3 font-mono text-xs leading-relaxed text-zinc-300">
            {result.public_key}
          </pre>
          <p className="mt-3 text-sm leading-relaxed text-zinc-500">
            You will paste this into your provider's <em>SSH key</em> field in a
            moment. Copy it now.
          </p>
        </div>
      )}

      <Footer>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="primary" busy={busy} disabled={!result} onClick={onNext}>
          Continue
        </Button>
      </Footer>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 3. Server — the human step
// ---------------------------------------------------------------------------

function ServerStep({
  publicKey,
  address,
  onAddress,
  sshUser,
  onSSHUser,
  caddyEmail,
  onCaddyEmail,
  onBack,
  onNext,
}: {
  publicKey: string;
  address: string;
  onAddress: (value: string) => void;
  sshUser: string;
  onSSHUser: (value: string) => void;
  caddyEmail: string;
  onCaddyEmail: (value: string) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const [showOptions, setShowOptions] = useState(false);
  const ready = address.trim().length > 0;

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Create the server</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        This is the one step OwnBase cannot do for you — providers require a human
        with a payment method. Any provider works; OwnBase has no provider
        integration and needs none.
      </p>

      <ul className="mt-4 space-y-2.5 text-sm leading-relaxed text-zinc-300">
        <Requirement>
          <strong className="font-medium text-zinc-100">Ubuntu 24.04</strong> (22.04
          also works).
        </Requirement>
        <Requirement>
          <strong className="font-medium text-zinc-100">
            At least 2 GB RAM and 20 GB disk.
          </strong>{" "}
          Services are cheap at rest; what sets the floor is that each one is built
          from source on the Base, and a build peaks far above the container it
          produces.
        </Requirement>
        <Requirement>
          <strong className="font-medium text-zinc-100">
            Your public key pasted into the provider's “SSH key” field
          </strong>{" "}
          as you create the machine. Most providers cannot add one afterwards without
          rebuilding, so this has to happen now.
          {publicKey && (
            <span className="mt-2 flex">
              <CopyButton value={publicKey} label="Copy public key again" />
            </span>
          )}
        </Requirement>
        <Requirement>
          <strong className="font-medium text-zinc-100">Reachable as root over SSH</strong>
          , which is the default on nearly every provider image.
        </Requirement>
      </ul>

      <form
        className="mt-6 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) onNext();
        }}
      >
        <Field
          label="IP address"
          hint="From your provider's console, once the machine has finished booting."
        >
          <Input
            autoFocus
            value={address}
            onChange={(e) => onAddress(e.target.value)}
            placeholder="203.0.113.10"
            spellCheck={false}
          />
        </Field>

        <button
          type="button"
          onClick={() => setShowOptions((v) => !v)}
          className="text-sm text-zinc-500 hover:text-zinc-300"
        >
          {showOptions ? "Hide" : "Show"} the two things people sometimes change
        </button>

        {showOptions && (
          <div className="space-y-4 rounded-lg border border-zinc-800 p-4">
            <Field
              label="SSH user"
              hint="Only change this if your provider's image does not allow root."
            >
              <Input
                value={sshUser}
                onChange={(e) => onSSHUser(e.target.value)}
                spellCheck={false}
              />
            </Field>
            <Field
              label="TLS contact email"
              hint="The ACME contact for automatic certificates. Needed only once a service has a public domain, and settable later."
            >
              <Input
                type="email"
                value={caddyEmail}
                onChange={(e) => onCaddyEmail(e.target.value)}
                placeholder="you@example.com"
                spellCheck={false}
              />
            </Field>
          </div>
        )}

        <Footer>
          <Button type="button" variant="ghost" onClick={onBack}>
            Back
          </Button>
          <Button type="submit" variant="primary" disabled={!ready}>
            Install OwnBase
          </Button>
        </Footer>
      </form>
    </Card>
  );
}

function Requirement({ children }: { children: React.ReactNode }) {
  return (
    <li className="flex gap-2.5">
      <span aria-hidden className="mt-2 h-1.5 w-1.5 shrink-0 rounded-full bg-zinc-600" />
      <span>{children}</span>
    </li>
  );
}

// ---------------------------------------------------------------------------
// 4. Install
// ---------------------------------------------------------------------------

/**
 * The phases `create --wait` goes through, matched against its own progress
 * lines so the user sees where they are in a five-minute wait.
 *
 * The CLI's stderr is the source of truth here; these patterns are a reading of
 * it, and a line that matches nothing simply scrolls past in the log below. An
 * unrecognised phase therefore costs a checkmark, never correctness.
 */
const phases: Array<{ label: string; match: RegExp }> = [
  { label: "Waiting for the server to accept SSH", match: /waiting for ssh|accept ssh/i },
  { label: "Checking the machine is fit", match: /preflight|checking/i },
  { label: "Installing the daemon", match: /installing ownbase/i },
  { label: "Reading the API token", match: /api token/i },
  { label: "Registering the Base in your vault", match: /registered/i },
  { label: "Hardening the host", match: /hardening/i },
];

function InstallStep({
  base,
  address,
  sshUser,
  caddyEmail,
  onBack,
  onDone,
}: {
  base: string;
  address: string;
  sshUser: string;
  caddyEmail: string;
  onBack: () => void;
  onDone: () => void;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [reached, setReached] = useState<number>(-1);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(true);
  const handle = useRef<StreamHandle | null>(null);

  useEffect(() => {
    const onEvent = (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        const line = event.line;
        setLines((prev) => [...prev, line]);
        // Phases only ever advance. A retry loop that logs "waiting for SSH"
        // again must not walk the checklist backwards.
        setReached((prev) => {
          const hit = phases.findIndex((p) => p.match.test(line));
          return hit > prev ? hit : prev;
        });
      }
    };

    const stream = api.createBase(
      base.trim(),
      {
        remote: `${sshUser}@${address.trim()}`,
        caddyEmail: caddyEmail.trim() || undefined,
        sshUser,
      },
      onEvent,
    );
    handle.current = stream;

    stream.done
      .then((code) => {
        setRunning(false);
        if (code === 0) {
          setReached(phases.length);
          onDone();
        } else {
          setError(installFailure(code));
        }
      })
      .catch((err: unknown) => {
        setRunning(false);
        setError(err instanceof Error ? err.message : String(err));
      });

    return () => {
      // Leaving the screen must not leave a half-finished install running
      // against a machine the user thinks nothing is touching.
      void stream.cancel();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        {running ? "Installing OwnBase" : error ? "Install failed" : "Installed"}
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        Unattended from here. The daemon is installed and signature-verified, the
        host is hardened, and the Base is registered in your vault. The last phase
        is the daemon doing its first pass — Podman, the firewall, fail2ban,
        automatic security updates — which is why this waits rather than claiming
        to be done early.
      </p>

      <ol className="mt-5 space-y-2">
        {phases.map((phase, i) => (
          <li key={phase.label} className="flex items-center gap-2.5 text-sm">
            <span
              aria-hidden
              className={cx(
                "w-4 shrink-0 text-center font-mono text-xs",
                i < reached ? "text-emerald-400" : "text-zinc-600",
              )}
            >
              {i < reached ? "✓" : i === reached && running ? "•" : "·"}
            </span>
            <span className={i <= reached ? "text-zinc-200" : "text-zinc-500"}>
              {phase.label}
            </span>
          </li>
        ))}
      </ol>

      {error && (
        <div className="mt-5">
          <ErrorNote title="The install did not finish" detail={error} />
        </div>
      )}

      <div className="mt-5">
        <p className="mb-2 text-xs uppercase tracking-wide text-zinc-500">Output</p>
        <LogView lines={lines} className="max-h-64" />
      </div>

      <Footer>
        <Button variant="ghost" onClick={onBack} disabled={running}>
          Back
        </Button>
        {running ? (
          <Button
            variant="danger"
            onClick={() => {
              void handle.current?.cancel();
            }}
          >
            Stop
          </Button>
        ) : null}
      </Footer>
    </Card>
  );
}

/**
 * What a non-zero exit from `create` means, in the user's terms.
 *
 * The CLI's own message is already in the log above, so this adds what the exit
 * code alone tells us about where to look next.
 */
function installFailure(code: number): string {
  switch (code) {
    case 3:
      return "The server was checked before anything was changed on it, and it did not pass. Nothing was installed. The output above says which check failed — usually the Ubuntu version, the architecture, or the machine being too small.";
    case 4:
      return "The installer ran on the server and failed partway through. The output above has the installer's own error.";
    case 5:
      return "OwnBase was installed, but the daemon did not report healthy in time. The machine is probably still hardening — open the Base and check its status in a minute.";
    case 6:
      return "A Base with this name already points at a different machine. Rename this one, or remove the existing Base first.";
    case 7:
      return "The vault locked while this was running. Unlock it and try again.";
    default:
      return `The command exited ${code}. The output above has the details.`;
  }
}

// ---------------------------------------------------------------------------
// 5. Done
// ---------------------------------------------------------------------------

function DoneStep({ base, onOpen }: { base: string; onOpen: () => void }) {
  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        {base} is up and hardened
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        Nothing but SSH is exposed, so there is no rush to the next step. When you
        are ready, two things are worth doing.
      </p>

      <ul className="mt-4 space-y-3 text-sm leading-relaxed text-zinc-300">
        <Requirement>
          <strong className="font-medium text-zinc-100">Set up backups.</strong> Until
          you do, this Base has no proven way back from a lost disk.{" "}
          <CommandLine>ownbasectl backup setup {base}</CommandLine>
        </Requirement>
        <Requirement>
          <strong className="font-medium text-zinc-100">
            Point it at a config repo.
          </strong>{" "}
          <CommandLine>ownbasectl config setup {base}</CommandLine> — the repo is
          yours, and it is where what runs on this machine is decided.
        </Requirement>
      </ul>

      <Footer>
        <span />
        <Button variant="primary" onClick={onOpen}>
          Open {base}
        </Button>
      </Footer>
    </Card>
  );
}
