//! The OwnBase app.
//!
//! Everything the window shows comes from `ownbasectl --json`, and everything it
//! changes goes through `ownbasectl`. There is no second path to a Base and no
//! Rust code that understands vaults, SSH, or `ownbase.yaml` — see `cli.rs` for
//! why that boundary is where it is.

mod cli;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .manage(cli::Running::default())
        .invoke_handler(tauri::generate_handler![
            cli::cli_run,
            cli::cli_stream,
            cli::cli_cancel,
        ])
        .run(tauri::generate_context!())
        .expect("error while running the OwnBase app");
}
