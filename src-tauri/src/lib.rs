mod config;
mod docker;
mod sidecar;
mod tray;
mod window;

use config::AppConfig;
use docker::DockerStatus;
use sidecar::SidecarStatus;

#[tauri::command]
async fn get_app_config(state: tauri::State<'_, AppState>) -> Result<AppConfig, String> {
    let config = state.config.lock().map_err(|e| e.to_string())?;
    Ok(config.clone())
}

#[tauri::command]
async fn save_app_config(
    state: tauri::State<'_, AppState>,
    config: AppConfig,
) -> Result<(), String> {
    let mut current = state.config.lock().map_err(|e| e.to_string())?;
    *current = config.clone();
    config::save_config(&config).map_err(|e| e.to_string())
}

#[tauri::command]
async fn get_docker_status() -> Result<DockerStatus, String> {
    Ok(docker::check_docker())
}

#[tauri::command]
async fn get_sidecar_status(state: tauri::State<'_, AppState>) -> Result<SidecarStatus, String> {
    let manager = state.sidecar.lock().map_err(|e| e.to_string())?;
    Ok(manager.status())
}

#[tauri::command]
async fn set_mode(
    state: tauri::State<'_, AppState>,
    mode: String,
    target: Option<String>,
    fleet_key: Option<String>,
) -> Result<(), String> {
    let mut config = state.config.lock().map_err(|e| e.to_string())?;
    config.mode = mode;
    config.worker_target = target;
    config.fleet_key = fleet_key;
    config.first_run_complete = true;
    config::save_config(&config).map_err(|e| e.to_string())
}

#[tauri::command]
fn get_platform_info() -> PlatformInfo {
    docker::get_platform_info()
}

#[derive(Clone, serde::Serialize)]
struct PlatformInfo {
    os: String,
    arch: String,
    docker_install_url: String,
}

struct AppState {
    config: std::sync::Mutex<AppConfig>,
    sidecar: std::sync::Mutex<sidecar::SidecarManager>,
}

pub fn run() {
    let config = config::load_config().unwrap_or_default();
    let sidecar_manager = sidecar::SidecarManager::new();

    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(AppState {
            config: std::sync::Mutex::new(config),
            sidecar: std::sync::Mutex::new(sidecar_manager),
        })
        .invoke_handler(tauri::generate_handler![
            get_app_config,
            save_app_config,
            get_docker_status,
            get_sidecar_status,
            set_mode,
            get_platform_info,
        ])
        .setup(|app| {
            tray::setup_tray(app)?;
            let app_handle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                window::on_app_ready(app_handle).await;
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            window::handle_window_event(window, event);
        })
        .run(tauri::generate_context!())
        .expect("error running CashPilot Desktop");
}
