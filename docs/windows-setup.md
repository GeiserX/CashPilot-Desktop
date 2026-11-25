# Windows setup: the Docker checklist

Most apps in the catalog run inside Docker. On Windows, Docker means Docker Desktop, and Docker Desktop needs several things from your PC. CashPilot checks each one and shows the result as a checklist.

## Where you find it

- On the first run, on the Runtime step, under the runtime card.
- In the app: click the runtime status at the top right (Runtime ready, Native mode or Runtime offline). On a narrow window it is hidden.
- When the Docker engine is unavailable or local setup still needs you, click Set up Docker in the Dashboard's Needs attention list or in Settings > Runtime.
- When the Docker engine is unavailable, the setup card for a Docker app also shows Set up Docker with This computer selected.

Each step has a state:

- **Done**: CashPilot read it, and it is fine.
- **Needs you**: something on your PC must change. The step says what and offers the next safe action.
- **Waits for an earlier step**: finish the step it names first.
- **Not known**: CashPilot could not read it. It never calls a step done unless it read it.
- **Worth knowing**: advice that blocks nothing CashPilot can prove. The summary does not count it as a step to finish.

Click **Check again** after you change something. CashPilot then reads every step again.

A Docker engine that answers proves a step only when it is this PC's own Docker Desktop: no `DOCKER_HOST` and no Docker context that points elsewhere. Windows containers prove only the steps they need, not WSL, virtualization or the advice rows. A guided setup ends when this PC's own Docker Desktop runs Linux containers.

## The steps

| Step | What CashPilot checks | What it offers |
|---|---|---|
| Windows version | The Windows build. Docker needs Windows 11 23H2 (build 22631) or Windows 10 22H2 (build 19045). Windows 10 still works, but gets no security updates since October 2025. | Open Windows Update. |
| Virtualization in the BIOS | Whether Windows already runs a hypervisor, else the processor's firmware flag. It also checks that the processor has SLAT. | Restart into the BIOS, after a confirm screen. The names of the setting and the usual keys. |
| WSL 2 backend: Windows features | The Virtual Machine Platform feature. Docker on its Hyper-V backend skips this step. | **Turn on WSL**: `wsl --install --no-distribution`, with one permission prompt, then a restart. No Linux distribution is installed. If the download hangs or fails at the Microsoft Store, a retry with a direct download (`--web-download`). A work or school policy that blocks WSL stops the step with an explanation. |
| WSL 2 backend: WSL version | The WSL version. Docker needs 2.1.5 or newer. | **Update WSL**: `wsl --update`, with one permission prompt, and the same direct-download retry. |
| Docker Desktop installed | Docker Desktop's install entry and program files. | **Install Docker Desktop**: a window where winget installs it, or Docker's download page. A PC without winget gets the download page. |
| Docker Desktop running | Whether Docker Desktop and its engine run. | Start Docker Desktop, then wait up to two minutes for its engine, with a progress line. |
| Docker's terms | Whether Docker Desktop was ever opened on your account, and whether its engine runs. | Bring Docker Desktop's window forward. You accept its terms there. |
| docker-users group | Shows only when Docker refuses your account. Whether your account is in the group, and whether this sign-in carries it. | **Add my account to docker-users**, with one permission prompt, then sign out and back in (or restart). When you sign in again, CashPilot checks that Windows gave your account the group. |
| Linux containers | Whether Docker runs Linux containers, which the apps need. | **Switch to Linux containers**, with Docker's own switch tool. It changes all your Docker work, so it runs only from its confirm screen. Without the tool, how to switch from Docker's menu. |
| Other virtual machine software | The VirtualBox and VMware drivers. They can slow Docker down. Shown as worth knowing, because nothing proves they stop Docker. When Windows' hypervisor is not running, although virtualization and the Virtual Machine Platform feature are on, the software may have turned it off at boot, and the step needs you instead. | **Check and turn Windows' hypervisor back on**, with one permission prompt. CashPilot first tries to read the boot option `hypervisorlaunchtype`. If the read works and it is already auto, nothing changes. If it is not auto, or Windows does not allow the read, CashPilot runs `bcdedit /set hypervisorlaunchtype auto`, and a restart follows. When the hypervisor is running, the row is a note only. |
| This PC is a virtual machine | The PC's maker and model. Inside a virtual machine, Docker needs nested virtualization from the outer machine. | While Docker does not run, the step needs you: the owner of the outer machine turns this VM off and turns on nested virtualization. On Hyper-V that is `Set-VMProcessor -VMName <name> -ExposeVirtualizationExtensions $true`. CashPilot cannot do this from inside the VM. Once this PC's own Docker Desktop runs Linux containers, the step is done. |
| Windows Server service | The start type of the Server service (LanmanServer). Docker needs it set to Automatic. While this PC's own Docker Desktop runs Linux containers, a service on Manual or turned off is only worth knowing. | **Set it to Automatic**, and start it, with one permission prompt. |

## Changes CashPilot makes for you

Each change in bold above runs only from its own confirm screen. The screen says:

- what will change on your PC;
- whether Windows shows a permission prompt. For the changes that need an administrator, the prompt names Windows PowerShell, because CashPilot makes the change through it. If your account is not an administrator, an administrator types their password there. The Docker Desktop installer asks for permission itself;
- whether a restart or a sign-out follows;
- how to undo the change.

**Cancel** changes nothing. While a change runs, a line counts the seconds, and the change stops by itself after its time limit (15 minutes for WSL, 2 minutes for most others). If it fails, the step stays **Needs you** and says why, with Windows' code.

CashPilot checks your PC again before it runs a change. If the step no longer asks for it, nothing runs. It adds only your own account to docker-users, by its ID (SID), which it reads before the permission prompt. So it is your account that joins, even when an administrator types their password.

## Restarts

Turning on WSL and turning Windows' hypervisor back on need a restart. Joining docker-users needs a sign-out and back in. When a change needs one, CashPilot saves where the checklist stands first, then shows a screen with **Restart now** (or **Sign out now**) and **Later**. That screen says that Windows closes every open app, so save your work first. Nothing restarts or signs out until you click.

Turning on virtualization needs a restart into the BIOS. When you click **Restart into BIOS**, a confirm screen comes first. It says that Windows closes every open app, so save your work first. Only **Restart now** on that screen restarts the PC.

Before the restart, CashPilot saves where the checklist stands. It also asks Windows to open CashPilot once at your next sign-in. When you sign in again, CashPilot opens on the checklist and reads every step again:

- If the PC restarted, it says so.
- If the PC did not restart yet, it says **Restart to finish**.

If your PC cannot restart into its BIOS by itself (an older BIOS, or a work policy), CashPilot says so and nothing restarts. On a UEFI PC, Windows can still open the BIOS for you: open Settings, System, Recovery, and click **Restart now** next to Advanced startup. Then choose Troubleshoot, Advanced options, UEFI Firmware Settings. If that option is not there, restart the PC yourself and press the BIOS key at power-on: often F2 or Del, F10 or Esc on HP, F1 on some Lenovo PCs.

Click **Stop guiding me after restarts** to cancel. CashPilot removes what it saved, and Windows does not open it at the next sign-in.

## Skip, use native apps only

If you do not want Docker, click **Skip, use native apps only**. On Windows, only Mysterium runs without Docker. Every other app in the catalog stays unavailable until Docker runs. You can open the checklist again at any time from the runtime status.

## What CashPilot never does

- It never restarts your PC, signs you out or restarts into the BIOS without your click on the confirm screen.
- It never changes a BIOS setting, and never says a BIOS setting is on unless it read it.
- It never installs Docker Desktop or WSL without your click on the confirm screen, never installs either silently, never installs a Linux distribution, and never accepts Docker's terms. winget's Docker Desktop package would accept them, so CashPilot replaces the installer's options with plain `install`.
- It never switches Docker between Linux and Windows containers without your click.
- It never adds anyone but your own account to a group, never adds anyone to Administrators, and adds you to docker-users only when Docker refused your account.
- It never sets `hypervisorlaunchtype` to off.
- It never turns off Windows security features: Memory Integrity, Credential Guard, Defender or the firewall.
- It never touches your WSL distributions, virtual machines or virtual networks.
- It reads only values Windows does not translate, so the checklist works the same in every language.
