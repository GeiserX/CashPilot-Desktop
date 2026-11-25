# Windows setup: the Docker checklist

Most apps in the catalog run inside Docker. On Windows, Docker means Docker Desktop, and Docker Desktop needs several things from your PC. CashPilot checks each one and shows the result as a checklist.

## Where you find it

- On the first run, on the Runtime step, under the runtime card.
- In the app: click the runtime status at the top right (Runtime ready, Native mode or Runtime offline).

Each step has a state:

- **Done**: CashPilot read it, and it is fine.
- **Needs you**: something on your PC must change. The step says what and offers the next safe action.
- **Waits for an earlier step**: finish the step it names first.
- **Not known**: CashPilot could not read it. It never calls a step done unless it read it.
- **Worth knowing**: advice that blocks nothing CashPilot can prove. The summary does not count it as a step to finish.

Click **Check again** after you change something. CashPilot then reads every step again.

## The steps

| Step | What CashPilot checks | What it offers |
|---|---|---|
| Windows version | The Windows build. Docker needs Windows 11 23H2 (build 22631) or Windows 10 22H2 (build 19045). Windows 10 still works, but gets no security updates since October 2025. | Open Windows Update. |
| Virtualization in the BIOS | Whether Windows already runs a hypervisor, else the processor's firmware flag. It also checks that the processor has SLAT. | Restart into the BIOS, after a confirm screen. The names of the setting and the usual keys. |
| WSL 2 backend: Windows features | The Virtual Machine Platform feature. Docker on its Hyper-V backend skips this step. | How to turn on WSL as an administrator. |
| WSL 2 backend: WSL version | The WSL version. Docker needs 2.1.5 or newer. | How to run `wsl --update`. |
| Docker Desktop installed | Docker Desktop's install entry and program files. | Open Docker's download page. |
| Docker Desktop running | Whether Docker Desktop and its engine run. | Start Docker Desktop, then wait up to two minutes for its engine, with a progress line. |
| Docker's terms | Whether Docker Desktop was ever opened on your account, and whether its engine runs. | Bring Docker Desktop's window forward. You accept its terms there. |
| docker-users group | Shows only when Docker refuses your account. Whether your account is in the group, and whether this sign-in carries it. | Sign out and back in, or how an administrator adds you. |
| Linux containers | Whether Docker runs Linux containers, which the apps need. | How to switch in Docker Desktop. |
| Other virtual machine software | The VirtualBox and VMware drivers. They can slow Docker down. Shown as worth knowing, because nothing proves they stop Docker. | A note only. |
| This PC is a virtual machine | The PC's maker and model. Inside a virtual machine, Docker needs nested virtualization from the outer machine. | A note only. |
| Windows Server service | The start type of the Server service (LanmanServer). Docker needs it set to Automatic. While Docker runs, a service on Manual or turned off is only worth knowing. | How to set it as an administrator. |

Steps that need an administrator show **Coming in the next version**, with the way to do them by hand.

## Restarts

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
- It never installs Docker Desktop, WSL or a Linux distribution for you, and never accepts Docker's terms.
- It never switches Docker between Linux and Windows containers.
- It never turns off Windows security features: Memory Integrity, Credential Guard, Defender or the firewall.
- It never touches your WSL distributions, virtual machines or virtual networks.
- It reads only values Windows does not translate, so the checklist works the same in every language.
