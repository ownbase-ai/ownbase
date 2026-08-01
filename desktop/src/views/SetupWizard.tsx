import { useCallback, useEffect, useRef, useState } from "react";
import { open } from "@tauri-apps/plugin-dialog";

import { CopyButton } from "../components/CopyButton";
import { ConfigSetupForm } from "./BaseDetail";
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
 * Three shapes. A new remote server needs a provider to authorize a key before
 * the machine boots, so this cannot be one button: it is a key, a pause while
 * the user goes to their provider, and then an install that takes minutes. A
 * local Multipass VM is provisioned entirely here — no provider step. A server
 * that already runs OwnBase needs none of that — it already has a key it
 * trusts, so the whole thing is a connectivity check.
 */
type Mode = "create-remote" | "create-local" | "adopt";
type Step = "path" | "name" | "key" | "server" | "finish" | "done";

function stepsFor(mode: Mode): Array<{ id: Step; label: string }> {
  if (mode === "create-local") {
    return [
      { id: "name", label: "Name" },
      { id: "key", label: "SSH key" },
      { id: "finish", label: "Install" },
    ];
  }
  return [
    { id: "name", label: "Name" },
    { id: "key", label: "SSH key" },
    { id: "server", label: "Server" },
    { id: "finish", label: mode === "create-remote" ? "Install" : "Register" },
  ];
}

function isCreate(mode: Mode): boolean {
  return mode === "create-remote" || mode === "create-local";
}

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
  const [step, setStep] = useState<Step>("path");
  const [mode, setMode] = useState<Mode>("create-remote");
  const [name, setName] = useState("");
  const [key, setKey] = useState<KeygenResult | null>(null);
  const [keySource, setKeySource] = useState<"generated" | "imported" | null>(null);
  const [sshKeyPath, setSSHKeyPath] = useState(""); // adopt mode only
  const [address, setAddress] = useState("");
  const [sshUser, setSSHUser] = useState("root");
  const [sshPort, setSSHPort] = useState(22); // adopt mode only
  const [apiPort, setAPIPort] = useState(7070); // adopt mode only
  const [caddyEmail, setCaddyEmail] = useState("");

  return (
    // Scroll the full main pane so the scrollbar sits on the window edge,
    // not inset against the max-w-2xl content column.
    <div className="h-full min-h-0 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-2xl flex-col gap-6 px-8 py-10">
      <header>
        <h1 className="text-lg font-medium text-zinc-100">Set up a Base</h1>
        <p className="mt-1 text-sm leading-relaxed text-zinc-500">
          {step === "path"
            ? "A remote server, a local VM on this computer, or one that already runs OwnBase."
            : mode === "create-remote"
              ? "About ten minutes, most of it waiting. One step needs you to visit your server provider; the rest happens here."
              : mode === "create-local"
                ? "A Multipass VM on this computer. Needs Multipass installed; no provider, no public IP."
                : "A few seconds — verify the connection, and it's registered."}
        </p>
      </header>

      {step !== "path" && <Progress steps={stepsFor(mode)} step={step} />}

      {step === "path" && (
        <PathStep
          onChoose={(m) => {
            setMode(m);
            setStep("name");
          }}
          onCancel={onCancel}
        />
      )}

      {step === "name" && (
        <NameStep
          existingNames={existingNames}
          name={name}
          onName={setName}
          onNext={() => setStep("key")}
          onBack={() => setStep("path")}
        />
      )}

      {step === "key" && isCreate(mode) && (
        <KeyStep
          base={name}
          local={mode === "create-local"}
          result={key}
          source={keySource}
          onResult={(result, source) => {
            setKey(result);
            setKeySource(source);
          }}
          onBack={() => setStep("name")}
          onNext={() => setStep(mode === "create-local" ? "finish" : "server")}
        />
      )}

      {step === "key" && mode === "adopt" && (
        <AdoptKeyStep
          path={sshKeyPath}
          onPath={setSSHKeyPath}
          onBack={() => setStep("name")}
          onNext={() => setStep("server")}
        />
      )}

      {step === "server" && mode === "create-remote" && (
        <ServerStep
          publicKey={key?.public_key ?? ""}
          address={address}
          onAddress={setAddress}
          sshUser={sshUser}
          onSSHUser={setSSHUser}
          caddyEmail={caddyEmail}
          onCaddyEmail={setCaddyEmail}
          onBack={() => setStep("key")}
          onNext={() => setStep("finish")}
        />
      )}

      {step === "server" && mode === "adopt" && (
        <AdoptServerStep
          address={address}
          onAddress={setAddress}
          sshUser={sshUser}
          onSSHUser={setSSHUser}
          sshPort={sshPort}
          onSSHPort={setSSHPort}
          apiPort={apiPort}
          onAPIPort={setAPIPort}
          onBack={() => setStep("key")}
          onNext={() => setStep("finish")}
        />
      )}

      {step === "finish" && isCreate(mode) && (
        <InstallStep
          base={name}
          local={mode === "create-local"}
          address={address}
          sshUser={sshUser}
          caddyEmail={caddyEmail}
          onBack={() => setStep(mode === "create-local" ? "key" : "server")}
          onDone={() => setStep("done")}
        />
      )}

      {step === "finish" && mode === "adopt" && (
        <RegisterStep
          base={name}
          address={address}
          sshUser={sshUser}
          sshPort={sshPort}
          apiPort={apiPort}
          sshKeyPath={sshKeyPath}
          onBack={() => setStep("server")}
          onDone={() => setStep("done")}
        />
      )}

      {step === "done" && <DoneStep base={name} mode={mode} onOpen={() => onFinished(name)} />}
      </div>
    </div>
  );
}

function Progress({
  steps,
  step,
}: {
  steps: Array<{ id: Step; label: string }>;
  step: Step;
}) {
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
// 0. Path — new server, or one that already runs OwnBase
// ---------------------------------------------------------------------------

function PathStep({
  onChoose,
  onCancel,
}: {
  onChoose: (mode: Mode) => void;
  onCancel: () => void;
}) {
  return (
    <div>
      <div className="space-y-3">
        <PathOption
          title="Set up a new server"
          description="A machine with no OwnBase on it yet. You'll generate a key, paste it into your provider when you create the server, and OwnBase installs itself."
          onClick={() => onChoose("create-remote")}
        />
        <PathOption
          title="Local VM on this computer"
          description="A Multipass Ubuntu VM for trying OwnBase without a cloud bill. Needs Multipass installed. No public IP and no provider console."
          onClick={() => onChoose("create-local")}
        />
        <PathOption
          title="Add a server that's already running OwnBase"
          description="Someone else provisioned it, or it's already known to another copy of your vault. You'll point at it with the key it already trusts — nothing to install."
          onClick={() => onChoose("adopt")}
        />
      </div>

      <Footer>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <span />
      </Footer>
    </div>
  );
}

function PathOption({
  title,
  description,
  onClick,
}: {
  title: string;
  description: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="w-full rounded-xl border border-zinc-800 bg-zinc-900/60 p-5 text-left transition-colors hover:border-emerald-500/40 hover:bg-zinc-900"
    >
      <h3 className="text-base font-medium text-zinc-100">{title}</h3>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">{description}</p>
    </button>
  );
}

// ---------------------------------------------------------------------------
// 1. Name
// ---------------------------------------------------------------------------

function NameStep({
  existingNames,
  name,
  onName,
  onNext,
  onBack,
}: {
  existingNames: string[];
  name: string;
  onName: (value: string) => void;
  onNext: () => void;
  onBack: () => void;
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
          <Button type="button" variant="ghost" onClick={onBack}>
            Back
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
// 2. Key — new server (generate, or import one you already have)
// ---------------------------------------------------------------------------

function KeyStep({
  base,
  local,
  result,
  source,
  onResult,
  onBack,
  onNext,
}: {
  base: string;
  local: boolean;
  result: KeygenResult | null;
  source: "generated" | "imported" | null;
  onResult: (result: KeygenResult, source: "generated" | "imported") => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const generate = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      onResult(await api.keygen(base.trim()), "generated");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [base, onResult]);

  const importFile = useCallback(async () => {
    const chosen = await open({
      multiple: false,
      directory: false,
      title: "Choose the private key file",
    });
    if (typeof chosen !== "string") return;
    setBusy(true);
    setError(null);
    try {
      onResult(await api.keygenImport(base.trim(), chosen), "imported");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }, [base, onResult]);

  const badge =
    result &&
    (source === "imported"
      ? { tone: "info" as const, label: "Key imported" }
      : result.created
        ? { tone: "good" as const, label: "New key created" }
        : { tone: "info" as const, label: "Existing key reused" });

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Your key for this Base</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        The private half is in your vault and will never be written to disk. When
        something needs to prove it is you, the credential agent signs for it. Each
        Base gets its own key, so retiring one Base revokes exactly one credential.
        {local && (
          <>
            {" "}
            For a local VM the key is injected when the VM is created — nothing to
            paste anywhere.
          </>
        )}
      </p>

      {!result && (
        <div className="mt-4 flex flex-col gap-2.5 sm:flex-row">
          <Button variant="primary" busy={busy} onClick={generate}>
            Generate a new key
          </Button>
          <Button variant="secondary" busy={busy} onClick={importFile}>
            I already have a key
          </Button>
        </div>
      )}

      {error && (
        <div className="mt-4">
          <ErrorNote title="Could not set up the key" detail={error} />
        </div>
      )}

      {result && badge && (
        <div className="mt-4">
          <div className="flex items-center justify-between gap-3">
            <Badge tone={badge.tone}>{badge.label}</Badge>
            {!local && <CopyButton value={result.public_key} label="Copy public key" />}
          </div>
          <pre className="selectable mt-3 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950 p-3 font-mono text-xs leading-relaxed text-zinc-300">
            {result.public_key}
          </pre>
          {!local && (
            <p className="mt-3 text-sm leading-relaxed text-zinc-500">
              You will paste this into your provider's <em>SSH key</em> field in a
              moment. Copy it now.
            </p>
          )}
        </div>
      )}

      <Footer>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button
          variant="primary"
          busy={busy}
          disabled={!result}
          onClick={onNext}
        >
          {local ? "Create the VM" : "Continue"}
        </Button>
      </Footer>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 2 (adopt). Key — the file this server already trusts
// ---------------------------------------------------------------------------

function AdoptKeyStep({
  path,
  onPath,
  onBack,
  onNext,
}: {
  path: string;
  onPath: (path: string) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  async function choose() {
    const chosen = await open({
      multiple: false,
      directory: false,
      title: "Choose the private key already authorized on this server",
    });
    if (typeof chosen === "string") onPath(chosen);
  }

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">The key this server already trusts</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        This has to be the private key already in that server's{" "}
        <code className="font-mono text-zinc-400">authorized_keys</code> — there is
        nothing to generate here, because a server that already exists has no way to
        learn about a brand new key. The file is only read to copy it into your
        vault; ownbasectl does that directly, so the key itself never passes through
        this window.
      </p>

      <div className="mt-4">
        <Button variant="secondary" onClick={choose}>
          {path ? "Choose a different file" : "Choose private key file…"}
        </Button>
        {path && (
          <div className="mt-3 rounded-lg border border-zinc-800 bg-zinc-950 p-3">
            <span className="selectable break-all font-mono text-xs text-zinc-300">{path}</span>
          </div>
        )}
      </div>

      <Footer>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="primary" disabled={!path} onClick={onNext}>
          Continue
        </Button>
      </Footer>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 3. Server — the human step (new server)
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

// ---------------------------------------------------------------------------
// 3 (adopt). Server — where it already is
// ---------------------------------------------------------------------------

function AdoptServerStep({
  address,
  onAddress,
  sshUser,
  onSSHUser,
  sshPort,
  onSSHPort,
  apiPort,
  onAPIPort,
  onBack,
  onNext,
}: {
  address: string;
  onAddress: (value: string) => void;
  sshUser: string;
  onSSHUser: (value: string) => void;
  sshPort: number;
  onSSHPort: (value: number) => void;
  apiPort: number;
  onAPIPort: (value: number) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const [showOptions, setShowOptions] = useState(false);
  const ready = address.trim().length > 0;

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">Where is it?</h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        Nothing is installed here. The next step only verifies the connection and,
        once that works, saves it — a mistyped host costs you nothing.
      </p>

      <ul className="mt-4 space-y-2.5 text-sm leading-relaxed text-zinc-300">
        <Requirement>
          <strong className="font-medium text-zinc-100">OwnBase already installed and running</strong>{" "}
          on it, however that happened.
        </Requirement>
        <Requirement>
          <strong className="font-medium text-zinc-100">Reachable over SSH</strong> with
          the key you just picked already in its <code className="font-mono text-zinc-400">authorized_keys</code>.
        </Requirement>
      </ul>

      <form
        className="mt-6 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) onNext();
        }}
      >
        <Field label="Host" hint="The hostname or IP address you'd SSH into.">
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
          {showOptions ? "Hide" : "Show"} advanced options
        </button>

        {showOptions && (
          <div className="space-y-4 rounded-lg border border-zinc-800 p-4">
            <Field
              label="SSH user"
              hint="Root by default; use whatever this server already expects."
            >
              <Input
                value={sshUser}
                onChange={(e) => onSSHUser(e.target.value)}
                spellCheck={false}
              />
            </Field>
            <Field label="SSH port" hint="Only if sshd listens somewhere other than 22.">
              <Input
                type="number"
                min={1}
                max={65535}
                value={sshPort}
                onChange={(e) => onSSHPort(Number(e.target.value) || 22)}
              />
            </Field>
            <Field
              label="API port"
              hint="Only if the daemon's status API was configured on a non-default port."
            >
              <Input
                type="number"
                min={1}
                max={65535}
                value={apiPort}
                onChange={(e) => onAPIPort(Number(e.target.value) || 7070)}
              />
            </Field>
          </div>
        )}

        <Footer>
          <Button type="button" variant="ghost" onClick={onBack}>
            Back
          </Button>
          <Button type="submit" variant="primary" disabled={!ready}>
            Verify and register
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
// 4. Install (new server)
// ---------------------------------------------------------------------------

/**
 * The phases `create --wait` goes through, matched against its own progress
 * lines so the user sees where they are in a five-minute wait.
 *
 * The CLI's stderr is the source of truth here; these patterns are a reading of
 * it, and a line that matches nothing simply scrolls past in the log below. An
 * unrecognised phase therefore costs a checkmark, never correctness.
 */
const remotePhases: Array<{ label: string; match: RegExp }> = [
  { label: "Waiting for the server to accept SSH", match: /waiting for ssh|accept ssh/i },
  { label: "Checking the machine is fit", match: /preflight|checking/i },
  { label: "Installing the daemon", match: /installing ownbase/i },
  { label: "Reading the API token", match: /api token/i },
  { label: "Registering the Base in your vault", match: /registered/i },
  { label: "Hardening the host", match: /hardening/i },
];

const localPhases: Array<{ label: string; match: RegExp }> = [
  { label: "Provisioning the local VM", match: /provisioning local vm/i },
  { label: "Launching the VM", match: /vm launched|waiting until the vm/i },
  { label: "Installing OwnBase inside the VM", match: /building ownbased|transferring|running the installer/i },
  { label: "Reading the API token", match: /reading the api token/i },
  { label: "Registering the Base in your vault", match: /registered/i },
  { label: "Hardening the host", match: /hardening/i },
];

function InstallStep({
  base,
  local,
  address,
  sshUser,
  caddyEmail,
  onBack,
  onDone,
}: {
  base: string;
  local: boolean;
  address: string;
  sshUser: string;
  caddyEmail: string;
  onBack: () => void;
  onDone: () => void;
}) {
  const phases = local ? localPhases : remotePhases;
  const [lines, setLines] = useState<string[]>([]);
  const [reached, setReached] = useState<number>(-1);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(true);
  const handle = useRef<StreamHandle | null>(null);

  useEffect(() => {
    // Defer the start past React Strict Mode's mount→unmount→mount cycle.
    // Starting multipass create immediately on mount races two installs
    // against the same VM name (delete/launch/transfer interleaved), which
    // is how "instance does not exist" and "is not running" show up together.
    let cancelled = false;
    let stream: StreamHandle | null = null;

    const timer = window.setTimeout(() => {
      if (cancelled) return;

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

      stream = api.createBase(
        base.trim(),
        local
          ? {}
          : {
              remote: `${sshUser}@${address.trim()}`,
              caddyEmail: caddyEmail.trim() || undefined,
              sshUser,
            },
        onEvent,
      );
      handle.current = stream;

      stream.done
        .then((code) => {
          if (cancelled) return;
          setRunning(false);
          if (code === 0) {
            setReached(phases.length);
            onDone();
          } else {
            setError(installFailure(code, local));
          }
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          setRunning(false);
          setError(err instanceof Error ? err.message : String(err));
        });
    }, 0);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      // Leaving the screen must not leave a half-finished install running
      // against a machine the user thinks nothing is touching.
      void stream?.cancel();
      handle.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        {running
          ? local
            ? "Creating the local VM"
            : "Installing OwnBase"
          : error
            ? "Install failed"
            : "Installed"}
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        {local
          ? "Unattended from here. Multipass launches an Ubuntu VM, OwnBase installs inside it, and the Base is registered in your vault. First launch may download an image."
          : "Unattended from here. The daemon is installed and signature-verified, the host is hardened, and the Base is registered in your vault. The last phase is the daemon doing its first pass — Podman, the firewall, fail2ban, automatic security updates — which is why this waits rather than claiming to be done early."}
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
function installFailure(code: number, local: boolean): string {
  switch (code) {
    case 3:
      return local
        ? "The local VM was checked before install finished, and something did not pass. The output above says which check failed."
        : "The server was checked before anything was changed on it, and it did not pass. Nothing was installed. The output above says which check failed — usually the Ubuntu version, the architecture, or the machine being too small.";
    case 4:
      return local
        ? "The installer ran inside the local VM and failed partway through. The output above has the installer's own error — often Multipass missing, or (on a dev build) Go not being able to build the daemon from this checkout."
        : "The installer ran on the server and failed partway through. The output above has the installer's own error.";
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
// 4 (adopt). Register
// ---------------------------------------------------------------------------

function RegisterStep({
  base,
  address,
  sshUser,
  sshPort,
  apiPort,
  sshKeyPath,
  onBack,
  onDone,
}: {
  base: string;
  address: string;
  sshUser: string;
  sshPort: number;
  apiPort: number;
  sshKeyPath: string;
  onBack: () => void;
  onDone: () => void;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState(true);
  const handle = useRef<StreamHandle | null>(null);

  useEffect(() => {
    const onEvent = (event: StreamEvent) => {
      if (event.kind === "stdout" || event.kind === "stderr") {
        setLines((prev) => [...prev, event.line]);
      }
    };

    const stream = api.adoptBase(
      base.trim(),
      { host: address.trim(), sshUser, sshPort, sshKeyPath, apiPort },
      onEvent,
    );
    handle.current = stream;

    stream.done
      .then((code) => {
        setRunning(false);
        if (code === 0) onDone();
        else setError(adoptFailure(code));
      })
      .catch((err: unknown) => {
        setRunning(false);
        setError(err instanceof Error ? err.message : String(err));
      });

    return () => {
      void stream.cancel();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        {running ? "Verifying and registering" : error ? "Could not register" : "Registered"}
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        The SSH connection is verified first — nothing is saved to your vault
        unless that works, so a mistyped host or an unauthorized key costs you
        nothing.
      </p>

      {error && (
        <div className="mt-5">
          <ErrorNote title="The registration did not finish" detail={error} />
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

/** What a non-zero exit from `adopt` means, in the user's terms. */
function adoptFailure(code: number): string {
  switch (code) {
    case 3:
      return "The server could not be verified, so nothing was saved. The output above says why — usually an unreachable host, or a key this server does not authorize.";
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

function DoneStep({ base, mode, onOpen }: { base: string; mode: Mode; onOpen: () => void }) {
  const [configDone, setConfigDone] = useState(false);

  return (
    <Card>
      <h2 className="text-base font-medium text-zinc-100">
        {isCreate(mode) ? `${base} is up and hardened` : `${base} is registered`}
      </h2>
      <p className="mt-1.5 text-sm leading-relaxed text-zinc-500">
        {mode === "create-local"
          ? "A Multipass VM on this computer. Nothing is exposed on the public internet."
          : isCreate(mode)
            ? "Nothing but SSH is exposed."
            : "It's in your vault now."}{" "}
        One more step makes it a real Base: point it at a config repo you own.
        That is where what runs is decided.
      </p>

      <div className="mt-4 rounded-lg border border-zinc-800 bg-zinc-950/40 p-4">
        <h3 className="text-sm font-medium text-zinc-100">Config repo</h3>
        {configDone ? (
          <p className="mt-2 text-sm text-emerald-300">
            Config source set. Open the Base to set up backups when you are ready.
          </p>
        ) : (
          <div className="mt-3">
            <ConfigSetupForm base={base} onDone={() => setConfigDone(true)} />
          </div>
        )}
      </div>

      <p className="mt-4 text-xs leading-relaxed text-zinc-500">
        Backups are next — until you turn them on, this Base has no proven way
        back from a lost disk. You can do that from the Base&apos;s Backups tab.
      </p>

      <Footer>
        <span />
        <Button variant="primary" onClick={onOpen}>
          Open {base}
        </Button>
      </Footer>
    </Card>
  );
}
