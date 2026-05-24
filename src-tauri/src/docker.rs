use std::process::Command;

use crate::PlatformInfo;

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq)]
pub enum DockerStatus {
    Available,
    NotInstalled,
    NotRunning,
}

pub fn check_docker() -> DockerStatus {
    let output = Command::new("docker").arg("info").output();

    match output {
        Ok(out) if out.status.success() => DockerStatus::Available,
        Ok(_) => DockerStatus::NotRunning,
        Err(_) => {
            if which::which("docker").is_ok() {
                DockerStatus::NotRunning
            } else {
                DockerStatus::NotInstalled
            }
        }
    }
}

pub fn get_platform_info() -> PlatformInfo {
    let os = std::env::consts::OS.to_string();
    let arch = std::env::consts::ARCH.to_string();

    let docker_install_url = match (os.as_str(), arch.as_str()) {
        ("macos", "aarch64") => {
            "https://desktop.docker.com/mac/main/arm64/Docker.dmg".to_string()
        }
        ("macos", _) => {
            "https://desktop.docker.com/mac/main/amd64/Docker.dmg".to_string()
        }
        ("windows", _) => {
            "https://desktop.docker.com/win/main/amd64/Docker%20Desktop%20Installer.exe".to_string()
        }
        ("linux", _) => "https://docs.docker.com/engine/install/".to_string(),
        _ => "https://docs.docker.com/get-docker/".to_string(),
    };

    PlatformInfo {
        os,
        arch,
        docker_install_url,
    }
}
