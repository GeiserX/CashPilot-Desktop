# FAQ

**How is this different from the CashPilot Docker container?**

It's the same passive-income management system, but packaged as a desktop app instead of a Docker container. You get system tray integration, auto-updates, a guided Docker installation wizard, and a native window -- no need to manage Docker yourself or access a web UI via browser.

**Do I still need Docker installed?**

Yes. CashPilot Desktop manages Docker containers for you, but Docker (or Podman) must be installed. The setup wizard detects if a compatible runtime is missing and guides you through installing Docker Desktop (macOS/Windows) or Docker Engine (Linux).

**How much can I earn?**

Honestly, this is beer money, not a salary. Earnings vary widely based on location, ISP, number of devices, and which services you run, but as a rough guide:

- A **single home connection** running a stack of bandwidth apps typically earns around **$5-25/month**.
- A **well-equipped household** that also shares spare storage or GPU time might reach **$30-75/month**.
- **Hundreds a month** is possible, but it takes a real fleet of machines, capable GPUs, or speculative token rewards -- not a single home connection.

Two things keep expectations realistic. Stacking many bandwidth apps on one connection hits **diminishing returns**, because they compete to sell the same idle bandwidth. And **DePIN token rewards are speculative** -- their value can drop sharply, so treat crypto payouts as a bet rather than a guarantee. The steadier earners are bandwidth, storage, and GPU compute. The dashboard tracks your actual earnings over time so you can see what works for your setup.

**Is it safe?**

All service containers run isolated via Docker. Credentials are stored in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). The app communicates only with localhost and the services you choose to deploy. No telemetry, no analytics, no data leaves your machine unless a service requires it.

**Desktop-only apps and your safety**

Some providers, such as Wipter and PassiveApp, have no Docker image. You install their own app on this computer, in your own session. CashPilot cannot fence these apps the way it fences containers. If such an app is hostile, or a bad update reaches it, the app can reach your home network and your Tailscale, VPN or work network. It can also read your files, your browsers' saved passwords and the logins that CashPilot keeps, and it can install other software.

For this reason, the setup card of a desktop app shows these risks first. The card shows the install steps and the setup guide only after you tick the box and click **Run in my session**. You accept once for each app, and CashPilot records the text version and the date in its config. When the text changes, the card asks again. To withdraw your consent, click **Withdraw** on the card or in Settings > Isolation. Withdrawing hides the install steps again. It does not uninstall the app.

A phone app does not run on this computer, so its card does not ask. Every other app without a Docker image asks, even when its catalog entry lists only a browser extension or no platform at all. Some of these lists are wrong: Teneo and UpRock, for example, list a browser extension but ship a Windows app.

Do not turn off antivirus scanning for the folder of an app, even if the app asks you to. With every earning app, other people's traffic leaves through your internet address. Your internet provider may forbid this, and abuse by a buyer is traced to your address.

**What happens if the app crashes?**

Docker containers continue running independently -- they don't stop when CashPilot Desktop is closed. Reopening the app reconnects to your running containers and resumes monitoring.

**Can I run CashPilot Desktop on multiple machines?**

Yes. Use **Worker Node** mode on additional machines -- they connect to your main CashPilot instance (either Desktop or Docker) and appear in the fleet dashboard. Each worker runs its own set of services and reports status back.
