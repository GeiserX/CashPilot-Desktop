use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppConfig {
    pub mode: String,
    pub sidecar_port: Option<u16>,
    pub worker_target: Option<String>,
    pub fleet_key: Option<String>,
    pub first_run_complete: bool,
    pub window_state: WindowState,
    pub auto_update: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WindowState {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
    pub maximized: bool,
}

impl Default for AppConfig {
    fn default() -> Self {
        Self {
            mode: "cashpilot".to_string(),
            sidecar_port: None,
            worker_target: None,
            fleet_key: None,
            first_run_complete: false,
            window_state: WindowState::default(),
            auto_update: true,
        }
    }
}

impl Default for WindowState {
    fn default() -> Self {
        Self {
            x: 0,
            y: 0,
            width: 1200,
            height: 800,
            maximized: false,
        }
    }
}

pub fn app_data_dir() -> PathBuf {
    let base = dirs::data_dir().unwrap_or_else(|| PathBuf::from("."));
    if cfg!(target_os = "macos") {
        dirs::home_dir()
            .unwrap_or_else(|| PathBuf::from("."))
            .join("Library/Application Support/com.cashpilot.desktop")
    } else if cfg!(target_os = "windows") {
        base.join("CashPilot Desktop")
    } else {
        base.join("cashpilot-desktop")
    }
}

pub fn backend_data_dir() -> PathBuf {
    app_data_dir().join("data")
}

fn config_path() -> PathBuf {
    app_data_dir().join("config.json")
}

pub fn load_config() -> Result<AppConfig, Box<dyn std::error::Error>> {
    let path = config_path();
    if !path.exists() {
        return Ok(AppConfig::default());
    }
    let contents = std::fs::read_to_string(path)?;
    let config: AppConfig = serde_json::from_str(&contents)?;
    Ok(config)
}

pub fn save_config(config: &AppConfig) -> Result<(), Box<dyn std::error::Error>> {
    let path = config_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let contents = serde_json::to_string_pretty(config)?;
    std::fs::write(path, contents)?;
    Ok(())
}
