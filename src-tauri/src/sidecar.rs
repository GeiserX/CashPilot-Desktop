use std::net::TcpListener;
use std::path::PathBuf;
use std::process::{Child, Command};
use std::time::Duration;

use crate::config;

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub enum SidecarStatus {
    Running { port: u16 },
    Starting,
    Stopped,
    Crashed { logs: String },
}

#[derive(Debug, Clone, PartialEq)]
pub enum Mode {
    CashPilot,
    Worker,
}

pub struct SidecarManager {
    child: Option<Child>,
    port: Option<u16>,
    status: SidecarStatus,
    restart_count: u32,
}

#[allow(dead_code)]
impl SidecarManager {
    pub fn new() -> Self {
        Self {
            child: None,
            port: None,
            status: SidecarStatus::Stopped,
            restart_count: 0,
        }
    }

    pub fn status(&self) -> SidecarStatus {
        self.status.clone()
    }

    pub fn port(&self) -> Option<u16> {
        self.port
    }

    pub fn spawn(&mut self, mode: Mode, sidecar_path: PathBuf) -> Result<u16, String> {
        let port = allocate_port().map_err(|e| format!("Port allocation failed: {e}"))?;
        let data_dir = config::backend_data_dir();

        std::fs::create_dir_all(&data_dir)
            .map_err(|e| format!("Failed to create data dir: {e}"))?;

        let entry = match mode {
            Mode::CashPilot => "desktop_main",
            Mode::Worker => "desktop_worker",
        };

        let child = Command::new(&sidecar_path)
            .arg(entry)
            .env("UVICORN_PORT", port.to_string())
            .env("DATA_DIR", data_dir.to_string_lossy().to_string())
            .env("CASHPILOT_HOST", "127.0.0.1")
            .spawn()
            .map_err(|e| format!("Failed to spawn sidecar: {e}"))?;

        self.child = Some(child);
        self.port = Some(port);
        self.status = SidecarStatus::Starting;
        self.restart_count = 0;

        Ok(port)
    }

    pub fn kill(&mut self) -> Result<(), String> {
        if let Some(mut child) = self.child.take() {
            // Confirm process is still alive before signalling
            match child.try_wait() {
                Ok(Some(_)) => {
                    // Already exited
                    self.status = SidecarStatus::Stopped;
                    self.port = None;
                    return Ok(());
                }
                Err(_) => {
                    self.status = SidecarStatus::Stopped;
                    self.port = None;
                    return Ok(());
                }
                Ok(None) => {} // Still running, proceed with shutdown
            }

            #[cfg(unix)]
            unsafe {
                libc::kill(child.id() as i32, libc::SIGTERM);
            }
            #[cfg(windows)]
            {
                let _ = child.kill();
            }

            let start = std::time::Instant::now();
            loop {
                match child.try_wait() {
                    Ok(Some(_)) => break,
                    Ok(None) if start.elapsed() > Duration::from_secs(5) => {
                        let _ = child.kill();
                        let _ = child.wait();
                        break;
                    }
                    Ok(None) => std::thread::sleep(Duration::from_millis(100)),
                    Err(_) => break,
                }
            }
        }
        self.status = SidecarStatus::Stopped;
        self.port = None;
        Ok(())
    }

    pub fn check_health(&self) -> bool {
        let Some(port) = self.port else {
            return false;
        };
        std::net::TcpStream::connect_timeout(
            &format!("127.0.0.1:{port}").parse().unwrap(),
            Duration::from_secs(1),
        )
        .is_ok()
    }

    pub fn mark_running(&mut self) {
        if let Some(port) = self.port {
            self.status = SidecarStatus::Running { port };
        }
    }

    pub fn mark_crashed(&mut self, logs: String) {
        self.status = SidecarStatus::Crashed { logs };
    }

    pub fn can_restart(&self) -> bool {
        self.restart_count < 3
    }

    pub fn increment_restart(&mut self) {
        self.restart_count += 1;
    }

    pub fn is_process_alive(&mut self) -> bool {
        if let Some(child) = self.child.as_mut() {
            matches!(child.try_wait(), Ok(None))
        } else {
            false
        }
    }
}

fn allocate_port() -> Result<u16, std::io::Error> {
    // Try multiple times to mitigate TOCTOU race (port reused between drop and sidecar bind)
    for _ in 0..5 {
        let listener = TcpListener::bind("127.0.0.1:0")?;
        let port = listener.local_addr()?.port();
        drop(listener);
        // Verify port is still available
        if TcpListener::bind(format!("127.0.0.1:{port}")).is_ok() {
            return Ok(port);
        }
    }
    // Fallback: return whatever the OS gives
    let listener = TcpListener::bind("127.0.0.1:0")?;
    let port = listener.local_addr()?.port();
    Ok(port)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_allocate_port_returns_valid_port() {
        let port = allocate_port().unwrap();
        assert!(port > 0);
        // Ephemeral ports are typically above 1024
        assert!(port >= 1024, "expected ephemeral port, got {port}");
    }

    #[test]
    fn test_allocate_port_returns_unique_ports() {
        let port1 = allocate_port().unwrap();
        let port2 = allocate_port().unwrap();
        assert_ne!(port1, port2);
    }

    #[test]
    fn test_sidecar_manager_new_initial_state() {
        let manager = SidecarManager::new();
        assert!(manager.port().is_none());
        assert!(matches!(manager.status(), SidecarStatus::Stopped));
        assert!(manager.can_restart());
    }

    #[test]
    fn test_can_restart_under_limit() {
        let mut manager = SidecarManager::new();
        assert!(manager.can_restart());
        manager.increment_restart();
        assert!(manager.can_restart());
        manager.increment_restart();
        assert!(manager.can_restart());
    }

    #[test]
    fn test_can_restart_at_limit() {
        let mut manager = SidecarManager::new();
        manager.increment_restart();
        manager.increment_restart();
        manager.increment_restart();
        assert!(!manager.can_restart());
    }

    #[test]
    fn test_mark_running_sets_status() {
        let mut manager = SidecarManager::new();
        // Without a port, mark_running does nothing meaningful
        manager.mark_running();
        assert!(matches!(manager.status(), SidecarStatus::Stopped));

        // Simulate having a port
        manager.port = Some(8080);
        manager.mark_running();
        assert!(matches!(manager.status(), SidecarStatus::Running { port: 8080 }));
    }

    #[test]
    fn test_mark_crashed_sets_status() {
        let mut manager = SidecarManager::new();
        manager.mark_crashed("segfault".to_string());
        match manager.status() {
            SidecarStatus::Crashed { logs } => assert_eq!(logs, "segfault"),
            other => panic!("expected Crashed, got {:?}", other),
        }
    }

    #[test]
    fn test_check_health_no_port_returns_false() {
        let manager = SidecarManager::new();
        assert!(!manager.check_health());
    }

    #[test]
    fn test_is_process_alive_no_child() {
        let mut manager = SidecarManager::new();
        assert!(!manager.is_process_alive());
    }

    #[test]
    fn test_kill_without_child_succeeds() {
        let mut manager = SidecarManager::new();
        assert!(manager.kill().is_ok());
        assert!(matches!(manager.status(), SidecarStatus::Stopped));
        assert!(manager.port().is_none());
    }
}
