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
    if let Some(ref key) = fleet_key {
        store_fleet_key(key).map_err(|e| format!("Failed to store fleet key: {e}"))?;
    }
    let mut config = state.config.lock().map_err(|e| e.to_string())?;
    config.mode = mode;
    config.worker_target = target;
    config.fleet_key = None;
    config.first_run_complete = true;
    config::save_config(&config).map_err(|e| e.to_string())
}

#[tauri::command]
async fn test_worker_connection(url: String) -> Result<u16, String> {
    let parsed = url::Url::parse(&url).map_err(|e| format!("Invalid URL: {e}"))?;
    let scheme = parsed.scheme();
    if scheme != "http" && scheme != "https" {
        return Err("Only HTTP/HTTPS addresses are supported.".to_string());
    }
    let health_url = format!("{}/health", url.trim_end_matches('/'));
    let agent = ureq::Agent::config_builder()
        .timeout_global(Some(std::time::Duration::from_secs(5)))
        .build()
        .new_agent();
    let response = agent.get(&health_url)
        .call()
        .map_err(|e| format!("Could not reach the address: {e}"))?;
    Ok(response.status().as_u16())
}

fn store_fleet_key(key: &str) -> Result<(), String> {
    let entry = keyring::Entry::new("com.cashpilot.desktop", "fleet-key")
        .map_err(|e| e.to_string())?;
    entry.set_password(key).map_err(|e| e.to_string())
}

#[allow(dead_code)]
fn get_fleet_key() -> Result<Option<String>, String> {
    let entry = keyring::Entry::new("com.cashpilot.desktop", "fleet-key")
        .map_err(|e| e.to_string())?;
    match entry.get_password() {
        Ok(key) => Ok(Some(key)),
        Err(keyring::Error::NoEntry) => Ok(None),
        Err(e) => Err(e.to_string()),
    }
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
            test_worker_connection,
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
