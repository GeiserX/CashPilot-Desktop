use tauri::{AppHandle, Manager, WindowEvent};

use crate::config;
use crate::docker::{self, DockerStatus};
use crate::sidecar::{HealthResult, Mode};
use crate::AppState;

const DEV_SIDECAR_PORT: u16 = 8765;

pub async fn on_app_ready(app: AppHandle) {
    let state = app.state::<AppState>();
    let config = match state.config.lock() {
        Ok(c) => c.clone(),
        Err(e) => {
            eprintln!("Config lock poisoned: {e}");
            return;
        }
    };

    let docker_status = docker::check_docker();
    if docker_status != DockerStatus::Available {
        return;
    }

    if !config.first_run_complete {
        return;
    }

    let mode = if config.mode == "worker" {
        Mode::Worker
    } else {
        Mode::CashPilot
    };

    if cfg!(dev) {
        // In dev mode, connect to a manually-started Python backend on a fixed port.
        // Run: cd /path/to/CashPilot && uvicorn app.main:app --host 127.0.0.1 --port 8765
        eprintln!("[DEV] Connecting to external Python backend on port {DEV_SIDECAR_PORT}");
        let start = std::time::Instant::now();
        while start.elapsed() < std::time::Duration::from_secs(10) {
            if std::net::TcpStream::connect_timeout(
                &format!("127.0.0.1:{DEV_SIDECAR_PORT}").parse().unwrap(),
                std::time::Duration::from_secs(1),
            )
            .is_ok()
            {
                if let Some(window) = app.get_webview_window("main") {
                    let url = format!("http://127.0.0.1:{DEV_SIDECAR_PORT}");
                    let _ = window.navigate(url.parse().unwrap());
                }
                return;
            }
            tokio::time::sleep(std::time::Duration::from_millis(500)).await;
        }
        eprintln!("[DEV] Python backend not reachable on port {DEV_SIDECAR_PORT}. Start it manually.");
        return;
    }

    let sidecar_path = resolve_sidecar_path(&app);
    let port = {
        let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
        manager.spawn(mode, sidecar_path)
    };

    match port {
        Ok(port) => {
            if wait_for_healthy(&state, &app, port).await {
                spawn_watchdog(app.clone());
            }
        }
        Err(e) => {
            eprintln!("Failed to spawn sidecar: {e}");
        }
    }
}

async fn wait_for_healthy(
    state: &tauri::State<'_, AppState>,
    app: &AppHandle,
    port: u16,
) -> bool {
    let start = std::time::Instant::now();
    while start.elapsed() < std::time::Duration::from_secs(10) {
        let result = {
            let manager = state.sidecar.lock().expect("sidecar lock poisoned");
            manager.check_health()
        };
        match result {
            HealthResult::Healthy => {
                let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                manager.mark_running();
                if let Some(window) = app.get_webview_window("main") {
                    let url = format!("http://127.0.0.1:{port}");
                    let _ = window.navigate(url.parse().unwrap());
                }
                return true;
            }
            HealthResult::VersionMismatch(v) => {
                eprintln!("Sidecar version mismatch: expected {}, got {v}", env!("CARGO_PKG_VERSION"));
                let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                manager.mark_crashed(format!("Version mismatch: {v}"));
                return false;
            }
            HealthResult::Unreachable => {}
        }
        tokio::time::sleep(std::time::Duration::from_millis(250)).await;
    }
    let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
    manager.mark_crashed("Health check timeout after 10s".to_string());
    eprintln!("Sidecar failed to become healthy within 10s");
    false
}

fn spawn_watchdog(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        loop {
            tokio::time::sleep(std::time::Duration::from_secs(5)).await;
            let state = app.state::<AppState>();
            let (alive, can_restart) = {
                let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                (manager.is_process_alive(), manager.can_restart())
            };
            if !alive {
                if can_restart {
                    eprintln!("Sidecar died unexpectedly, attempting restart...");
                    let port = {
                        let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                        manager.respawn()
                    };
                    if let Ok(port) = port {
                        if wait_for_healthy(&state, &app, port).await {
                            continue;
                        }
                    }
                    eprintln!("Sidecar restart failed");
                } else {
                    eprintln!("Sidecar crashed and max restarts (3) exceeded");
                }
                break;
            }
        }
    });
}

pub fn handle_window_event(window: &tauri::Window, event: &WindowEvent) {
    if let WindowEvent::CloseRequested { .. } = event {
        let state = window.state::<AppState>();

        if let Ok(pos) = window.outer_position() {
            if let Ok(size) = window.outer_size() {
                if let Ok(mut config) = state.config.lock() {
                    config.window_state.x = pos.x;
                    config.window_state.y = pos.y;
                    config.window_state.width = size.width;
                    config.window_state.height = size.height;
                    config.window_state.maximized = window.is_maximized().unwrap_or(false);
                    let _ = config::save_config(&config);
                }
            }
        }

        if let Ok(mut manager) = state.sidecar.lock() {
            let _ = manager.kill();
        };
    }
}

fn resolve_sidecar_path(app: &AppHandle) -> std::path::PathBuf {
    #[cfg(dev)]
    {
        let manifest_dir = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let dev_path = manifest_dir.join("sidecar").join("cashpilot-sidecar");
        if dev_path.exists() {
            return dev_path;
        }
    }
    app.path()
        .resource_dir()
        .unwrap_or_default()
        .join("sidecar")
        .join("cashpilot-sidecar")
}
