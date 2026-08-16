//! The app's only way to do anything: run the bundled `ownbasectl` and read
//! what it printed.
//!
//! This is deliberately the whole backend. `ownbasectl` already owns the
//! semantics — it holds the vault, signs with the credential agent, commits to
//! the config repo, and reconciles the Base. An app that reimplemented any of
//! that would be a second control plane that could disagree with the CLI, and
//! then "what is actually deployed" would have two answers. So the Rust side
//! spawns a process and forwards bytes.

use std::collections::HashMap;
use std::sync::Mutex;

use serde::Serialize;
use tauri::ipc::Channel;
use tauri::{AppHandle, State};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

/// The bundled CLI's name.
///
/// Not `binaries/ownbasectl`, which is how `externalBin` names it in
/// tauri.conf.json: that path is where the build *finds* the binary, and Tauri
/// resolves a sidecar as `<dir of the running executable>/<this>`. The bundle
/// puts it beside the app binary in `Contents/MacOS/`, so only the basename is
/// right — and the difference does not appear until the app actually runs, since
/// nothing about the build fails when this is wrong.
const SIDECAR: &str = "ownbasectl";

/// Subcommands the frontend may invoke.
///
/// The webview renders things that came off a Base — session transcripts,
/// service names, daemon output. If any of that ever managed to execute script,
/// an unrestricted bridge would let it run arbitrary commands as the user on
/// every machine they own. So the bridge names what it allows.
///
/// This is exactly the set [desktop/src/lib/api.ts](../../src/lib/api.ts)
/// calls today, not everything `ownbasectl` can do. The allowlist is the
/// stated XSS boundary, so a subcommand the UI does not use yet stays out
/// until some screen actually calls it; adding one here is one line, at the
/// point a caller in `api.ts` needs it.
///
/// `ssh` and `tunnel` are absent for a second reason on top of that: both take
/// an arbitrary command or hold an interactive session open, which is exactly
/// the shape of thing this list exists to keep out of the webview's reach.
/// The app reads *recordings* of sessions; it does not open them.
const ALLOWED: &[&str] = &[
    "adopt",
    "agent",
    "backup",
    "checkup",
    "config",
    "create",
    "db",
    "delete",
    "deploy",
    "keygen",
    "list",
    "restore",
    "secrets",
    "security",
    "self-update",
    "service",
    "sessions",
    "ssh-key",
    "upgrade",
    "vault",
    "version",
];

/// What one `ownbasectl` invocation produced.
#[derive(Debug, Serialize, Clone)]
pub struct CliResult {
    /// The process exit code. `ownbasectl` classifies failures, so this is
    /// meaningful: 7 means the vault is locked, 3 means preflight failed.
    pub code: i32,
    pub stdout: String,
    pub stderr: String,
}

/// One line of output from a still-running command, or its final result.
#[derive(Debug, Serialize, Clone)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum StreamEvent {
    /// Progress. `ownbasectl` sends progress and logs to stderr and results to
    /// stdout, so the two are kept apart rather than interleaved into one blob.
    Stdout {
        line: String,
    },
    Stderr {
        line: String,
    },
    Finished {
        code: i32,
    },
    /// The process could not be started or died without a status.
    Failed {
        message: String,
    },
}

/// Running streamed commands, so the UI can cancel one.
#[derive(Default)]
pub struct Running(Mutex<HashMap<String, CommandChild>>);

fn check_allowed(args: &[String]) -> Result<(), String> {
    let first = args
        .first()
        .ok_or_else(|| "no ownbasectl subcommand given".to_string())?;
    if !ALLOWED.contains(&first.as_str()) {
        return Err(format!(
            "the OwnBase app does not run 'ownbasectl {first}'. \
             Run it in a terminal if you need it."
        ));
    }
    Ok(())
}

/// Run `ownbasectl` to completion and return everything it printed.
///
/// A non-zero exit is returned rather than raised: `ownbasectl` exit codes are
/// information the UI acts on (7 shows the unlock screen, 3 explains a
/// preflight failure), not errors to collapse into a generic message.
#[tauri::command]
pub async fn cli_run(
    app: AppHandle,
    args: Vec<String>,
    stdin: Option<String>,
) -> Result<CliResult, String> {
    check_allowed(&args)?;

    let (mut rx, mut child) = app
        .shell()
        .sidecar(SIDECAR)
        .map_err(|e| format!("locate the bundled ownbasectl: {e}"))?
        .args(&args)
        .spawn()
        .map_err(|e| format!("run ownbasectl {}: {e}", args.join(" ")))?;

    // The master password goes in on stdin and never in argv, because argv is
    // readable by any process on the machine through `ps` for as long as this
    // one lives. Dropping the child afterwards closes the pipe, which is what
    // lets the CLI's read of stdin return instead of blocking forever.
    if let Some(input) = stdin {
        child
            .write(input.as_bytes())
            .map_err(|e| format!("send input to ownbasectl: {e}"))?;
    }
    drop(child);

    let mut code = None;
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    while let Some(event) = rx.recv().await {
        match event {
            CommandEvent::Terminated(payload) => code = payload.code,
            // The plugin reads line by line and strips the terminator, so it
            // goes back on here; a JSON document printed across several lines
            // has to reassemble exactly as it was written.
            CommandEvent::Stdout(line) => {
                stdout.extend(line);
                stdout.push(b'\n');
            }
            CommandEvent::Stderr(line) => {
                stderr.extend(line);
                stderr.push(b'\n');
            }
            _ => {}
        }
    }

    Ok(CliResult {
        code: code.unwrap_or(-1),
        stdout: String::from_utf8_lossy(&stdout).to_string(),
        stderr: String::from_utf8_lossy(&stderr).to_string(),
    })
}

/// Run `ownbasectl` and forward its output line by line as it arrives.
///
/// This exists for `create --wait`, which takes several minutes of real work —
/// waiting for SSH, preflight, install, hardening. Showing that as it happens
/// is the difference between a wizard that looks broken and one that looks like
/// it is doing something.
#[tauri::command]
pub async fn cli_stream(
    app: AppHandle,
    running: State<'_, Running>,
    id: String,
    args: Vec<String>,
    stdin: Option<String>,
    on_event: Channel<StreamEvent>,
) -> Result<(), String> {
    check_allowed(&args)?;

    let (mut rx, mut child) = app
        .shell()
        .sidecar(SIDECAR)
        .map_err(|e| format!("locate the bundled ownbasectl: {e}"))?
        .args(&args)
        .spawn()
        .map_err(|e| format!("start ownbasectl {}: {e}", args.join(" ")))?;

    // Same stdin rule as cli_run: secrets never in argv. Write then leave the
    // child alive so stdout/stderr keep streaming (unlike cli_run, which drops
    // the child handle after writing because it only needs the final result).
    if let Some(input) = stdin {
        child
            .write(input.as_bytes())
            .map_err(|e| format!("send input to ownbasectl: {e}"))?;
    }

    running.0.lock().unwrap().insert(id.clone(), child);

    while let Some(event) = rx.recv().await {
        let out = match event {
            CommandEvent::Stdout(bytes) => StreamEvent::Stdout {
                line: String::from_utf8_lossy(&bytes).trim_end().to_string(),
            },
            CommandEvent::Stderr(bytes) => StreamEvent::Stderr {
                line: String::from_utf8_lossy(&bytes).trim_end().to_string(),
            },
            CommandEvent::Terminated(payload) => StreamEvent::Finished {
                code: payload.code.unwrap_or(-1),
            },
            CommandEvent::Error(message) => StreamEvent::Failed { message },
            _ => continue,
        };
        // A closed channel means the window went away; there is nothing left to
        // report to, so stop rather than spin.
        if on_event.send(out).is_err() {
            break;
        }
    }

    running.0.lock().unwrap().remove(&id);
    Ok(())
}

/// Kill a streamed command. Used by the wizard's cancel button.
#[tauri::command]
pub fn cli_cancel(running: State<'_, Running>, id: String) -> Result<(), String> {
    if let Some(child) = running.0.lock().unwrap().remove(&id) {
        child.kill().map_err(|e| format!("stop the command: {e}"))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn allows_every_subcommand_api_ts_calls() {
        for cmd in [
            "adopt",
            "agent",
            "backup",
            "checkup",
            "config",
            "create",
            "db",
            "delete",
            "deploy",
            "keygen",
            "list",
            "restore",
            "secrets",
            "security",
            "self-update",
            "service",
            "sessions",
            "ssh-key",
            "upgrade",
            "vault",
            "version",
        ] {
            assert!(
                check_allowed(&args(&[cmd])).is_ok(),
                "{cmd} should be allowed"
            );
        }
    }

    #[test]
    fn refuses_high_impact_commands_the_ui_never_calls() {
        // These exist in ownbasectl but no screen in the app calls them today;
        // the allowlist is the XSS boundary, so they must stay out until one
        // does — see the ALLOWED doc comment. status/updates remain CLI-only
        // (their data arrives via checkup); compile/plan/apply are local-repo.
        for cmd in ["status", "updates", "compile", "plan", "apply"] {
            assert!(
                check_allowed(&args(&[cmd])).is_err(),
                "{cmd} should be refused"
            );
        }
    }

    #[test]
    fn refuses_ssh_and_tunnel() {
        // Both take an arbitrary command or hold an interactive session open —
        // exactly what this allowlist exists to keep out of the webview.
        assert!(check_allowed(&args(&["ssh"])).is_err());
        assert!(check_allowed(&args(&["tunnel"])).is_err());
    }

    #[test]
    fn refuses_empty_argv() {
        assert!(check_allowed(&[]).is_err());
    }
}
