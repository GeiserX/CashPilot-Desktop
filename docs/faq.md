# FAQ

**How is this different from the CashPilot Docker container?**

It's the same passive-income management system, but packaged as a desktop app instead of a Docker container. You get system tray integration, a guided Docker setup checklist on Windows, and a native window -- no need to manage Docker yourself or access a web UI via browser. It does not update itself yet: download each new version from the [releases page](https://github.com/GeiserX/CashPilot-Desktop/releases).

**Do I still need Docker installed?**

Yes, for most apps. CashPilot Desktop manages Docker containers for you, but Docker (or Podman) must be installed. Without Docker, only the apps with a native build run (on Windows, Mysterium).

On Windows, the first run and the runtime status at the top of the app open a checklist. It checks what Docker Desktop needs from your PC: the Windows version, virtualization in the BIOS, WSL, the Docker Desktop install and its terms, and a few more. Each step says what it means for your PC and offers the next safe action, such as opening Docker's download page, starting Docker Desktop, or restarting into the BIOS after you confirm. It never installs Docker or WSL for you, never accepts Docker's terms, and never changes a BIOS setting. Steps that need an administrator say how to do them by hand for now. See [Windows setup](windows-setup.md).

On macOS and Linux, the first run detects a missing runtime and shows install guides for Docker Desktop (macOS) or Docker Engine (Linux).

**How much can I earn?**

Honestly, this is beer money, not a salary. Earnings vary widely based on location, ISP, number of devices, and which services you run, but as a rough guide:

- A **single home connection** running a stack of bandwidth apps typically earns around **$5-25/month**.
- A **well-equipped household** that also shares spare storage or GPU time might reach **$30-75/month**.
- **Hundreds a month** is possible, but it takes a real fleet of machines, capable GPUs, or speculative token rewards -- not a single home connection.

Two things keep expectations realistic. Stacking many bandwidth apps on one connection hits **diminishing returns**, because they compete to sell the same idle bandwidth. And **DePIN token rewards are speculative** -- their value can drop sharply, so treat crypto payouts as a bet rather than a guarantee. The steadier earners are bandwidth, storage, and GPU compute. The dashboard tracks your actual earnings over time so you can see what works for your setup.

**Is it safe?**

All service containers run isolated via Docker. Credentials are stored in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). The app communicates with localhost, the services you choose to deploy and the CashPilot server you joined, if any. It also asks api.ipify.org for this computer's public IP (for the LAN fence and proxy checks), and CoinGecko and Frankfurter for exchange rates. The Windows VM guard also asks icanhazip.com for the public IP. While Activity is on, the app downloads the iptoasn.com network table once a week ([details](activity.md#network-names-and-countries)). To show your balance, it asks the services and mining pools you set up, with the login or payout address you saved. While the server-commands switch is on, that server can also deploy and stop services here, through TCP 8086 on private network and Tailscale addresses only ([details](configuration.md#the-server-starts-and-stops-services-here)). There is no telemetry and no analytics. Apart from these requests, data leaves your machine only when a service you deploy sends it.

**Desktop-only apps and your safety**

Some providers, such as Wipter and PassiveApp, have no Docker image. You install their own app on this computer, in your own session. CashPilot cannot fence these apps the way it fences containers. If such an app is hostile, or a bad update reaches it, the app can reach your home network and your Tailscale, VPN or work network. It can also read your files, your browsers' saved passwords and the logins that CashPilot keeps, and it can install other software.

For this reason, the setup card of a desktop app shows these risks first. The card shows the install steps and the setup guide only after you tick the box and click **Run in my session**. You accept once for each app, and CashPilot records the text version and the date in its config. When the text changes, the card asks again. To withdraw your consent, click **Withdraw** on the card or in Settings > Isolation. Withdrawing hides the install steps again. It does not uninstall the app.

A phone app does not run on this computer, so its card does not ask. Every other app without a Docker image asks, even when its catalog entry lists only a browser extension or no platform at all. Some of these lists are wrong: Teneo and UpRock, for example, list a browser extension but ship a Windows app.

Do not turn off antivirus scanning for the folder of an app, even if the app asks you to. With every earning app, other people's traffic leaves through your internet address. Your internet provider may forbid this, and abuse by a buyer is traced to your address.

**What happens if the app crashes?**

Docker containers continue running independently -- they don't stop when CashPilot Desktop is closed. Reopening the app reconnects to your running containers and resumes monitoring.

**Can I run CashPilot Desktop on multiple machines?**

Yes. Pick **Join my CashPilot** on the other machines -- they join your CashPilot server and appear on its Fleet page. Each one runs its own set of services and reports what runs there; the server collects and shows the earnings. A Desktop cannot join another Desktop.

**How do I uninstall CashPilot Desktop on Windows?**

Use Settings > Apps > Installed apps, as for any app. The uninstaller removes the app, its shortcuts and its background helper. It asks whether to also stop and remove the earning apps it started, and whether to delete your data. By default it keeps both, so your containers keep earning and a new install finds your data. It never touches containers that CashPilot Desktop did not make. The details are in [Getting started](getting-started.md#uninstall-on-windows).
