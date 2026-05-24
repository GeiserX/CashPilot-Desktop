const { invoke } = window.__TAURI__.core;

async function init() {
    const statusText = document.getElementById('status-text');

    // Check Docker status
    statusText.textContent = 'Checking Docker...';
    const dockerStatus = await invoke('get_docker_status');

    if (dockerStatus !== 'Available') {
        window.location.href = 'pages/docker-check.html';
        return;
    }

    // Check if first run
    const config = await invoke('get_app_config');
    if (!config.first_run_complete) {
        window.location.href = 'pages/wizard.html';
        return;
    }

    // Wait for sidecar to be ready
    statusText.textContent = 'Starting CashPilot...';
    await waitForSidecar();
}

async function waitForSidecar() {
    const statusText = document.getElementById('status-text');
    let attempts = 0;
    const maxAttempts = 25; // 5 seconds at 200ms intervals

    while (attempts < maxAttempts) {
        const status = await invoke('get_sidecar_status');
        if (status.Running) {
            // Redirect to sidecar UI
            window.location.href = `http://127.0.0.1:${status.Running.port}`;
            return;
        }
        if (status.Crashed) {
            statusText.textContent = 'Failed to start. Check logs.';
            return;
        }
        attempts++;
        await new Promise(r => setTimeout(r, 200));
    }
    statusText.textContent = 'Sidecar timeout. Please restart.';
}

init();
