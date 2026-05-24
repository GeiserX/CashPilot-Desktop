use tauri::{AppHandle, Manager, WindowEvent};

use crate::config;
use crate::docker::{self, DockerStatus};
use crate::sidecar::Mode;
use crate::AppState;

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

    let sidecar_path = resolve_sidecar_path(&app);
    let port = {
        let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
        manager.spawn(mode, sidecar_path)
    };

    match port {
        Ok(port) => {
            let start = std::time::Instant::now();
            let mut healthy = false;
            while start.elapsed() < std::time::Duration::from_secs(5) {
                let check = {
                    let manager = state.sidecar.lock().expect("sidecar lock poisoned");
                    manager.check_health()
                };
                if check {
                    healthy = true;
                    let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                    manager.mark_running();
                    if let Some(window) = app.get_webview_window("main") {
                        let url = format!("http://127.0.0.1:{port}");
                        let _ = window.navigate(url.parse().unwrap());
                    }
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(200)).await;
            }
            if !healthy {
                let mut manager = state.sidecar.lock().expect("sidecar lock poisoned");
                manager.mark_crashed("Health check timeout after 5s".to_string());
                eprintln!("Sidecar failed to become healthy within 5s");
            }
        }
        Err(e) => {
            eprintln!("Failed to spawn sidecar: {e}");
        }
    }
}

pub fn handle_window_event(window: &tauri::Window, event: &WindowEvent) {
    if let WindowEvent::CloseRequested { .. } = event {
        if let Ok(pos) = window.outer_position() {
            if let Ok(size) = window.outer_size() {
                let state = window.state::<AppState>();
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

        let state = window.state::<AppState>();
        if let Ok(mut manager) = state.sidecar.lock() {
            let _ = manager.kill();
        }
    }
}

fn resolve_sidecar_path(app: &AppHandle) -> std::path::PathBuf {
    app.path()
        .resource_dir()
        .unwrap_or_default()
        .join("sidecar")
        .join("cashpilot-sidecar")
}
