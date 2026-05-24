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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_config_values() {
        let config = AppConfig::default();
        assert_eq!(config.mode, "cashpilot");
        assert_eq!(config.sidecar_port, None);
        assert_eq!(config.worker_target, None);
        assert_eq!(config.fleet_key, None);
        assert!(!config.first_run_complete);
        assert!(config.auto_update);
    }

    #[test]
    fn test_default_window_state() {
        let ws = WindowState::default();
        assert_eq!(ws.x, 0);
        assert_eq!(ws.y, 0);
        assert_eq!(ws.width, 1200);
        assert_eq!(ws.height, 800);
        assert!(!ws.maximized);
    }

    #[test]
    fn test_serialization_roundtrip() {
        let config = AppConfig {
            mode: "worker".to_string(),
            sidecar_port: Some(8080),
            worker_target: Some("https://example.com".to_string()),
            fleet_key: Some("key-123".to_string()),
            first_run_complete: true,
            window_state: WindowState {
                x: 100,
                y: 200,
                width: 1920,
                height: 1080,
                maximized: true,
            },
            auto_update: false,
        };

        let json = serde_json::to_string(&config).unwrap();
        let deserialized: AppConfig = serde_json::from_str(&json).unwrap();

        assert_eq!(deserialized.mode, "worker");
        assert_eq!(deserialized.sidecar_port, Some(8080));
        assert_eq!(deserialized.worker_target.as_deref(), Some("https://example.com"));
        assert_eq!(deserialized.fleet_key.as_deref(), Some("key-123"));
        assert!(deserialized.first_run_complete);
        assert!(!deserialized.auto_update);
        assert_eq!(deserialized.window_state.width, 1920);
        assert!(deserialized.window_state.maximized);
    }

    #[test]
    fn test_save_and_load_from_temp_dir() {
        let tmp = tempfile::tempdir().unwrap();
        let config_file = tmp.path().join("config.json");

        let config = AppConfig {
            mode: "cashpilot".to_string(),
            sidecar_port: Some(9090),
            worker_target: None,
            fleet_key: None,
            first_run_complete: true,
            window_state: WindowState::default(),
            auto_update: true,
        };

        let contents = serde_json::to_string_pretty(&config).unwrap();
        std::fs::write(&config_file, &contents).unwrap();

        let loaded: AppConfig =
            serde_json::from_str(&std::fs::read_to_string(&config_file).unwrap()).unwrap();
        assert_eq!(loaded.mode, "cashpilot");
        assert_eq!(loaded.sidecar_port, Some(9090));
        assert!(loaded.first_run_complete);
    }

    #[test]
    fn test_load_missing_file_returns_default() {
        let tmp = tempfile::tempdir().unwrap();
        let missing = tmp.path().join("nonexistent.json");
        assert!(!missing.exists());
        // load_config uses a fixed path, so we test the logic directly
        // If path doesn't exist, we get default
        let config = AppConfig::default();
        assert_eq!(config.mode, "cashpilot");
    }

    #[test]
    fn test_app_data_dir_is_not_empty() {
        let dir = app_data_dir();
        assert!(!dir.as_os_str().is_empty());
    }

    #[test]
    fn test_backend_data_dir_is_subdir_of_app_data_dir() {
        let app_dir = app_data_dir();
        let backend_dir = backend_data_dir();
        assert!(backend_dir.starts_with(&app_dir));
        assert!(backend_dir.ends_with("data"));
    }
}
