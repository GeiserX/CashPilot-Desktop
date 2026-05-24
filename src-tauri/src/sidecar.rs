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
            // Try graceful shutdown first
            #[cfg(unix)]
            unsafe {
                libc::kill(child.id() as i32, libc::SIGTERM);
            }
            #[cfg(windows)]
            {
                let _ = child.kill();
            }

            // Wait up to 5 seconds for graceful exit
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
    let listener = TcpListener::bind("127.0.0.1:0")?;
    let port = listener.local_addr()?.port();
    drop(listener);
    Ok(port)
}
