# What's new in 2.0

In CashPilot Desktop 2.0, a CashPilot server that this computer joined can
deploy, stop, start, restart and remove services here. We call that server
commands. Server commands need CashPilot 3.0.0 or newer on the server.

2.0 is a major release because some changes need a step from you. Read
[Before you upgrade](#before-you-upgrade) and
[After you upgrade](#after-you-upgrade) first. The rest of this page lists
what changed.

## Which server you need

| Server | Desktop 1.7.0 | Desktop 2.0 |
|---|---|---|
| CashPilot 2.x | Reports only | Reports only |
| CashPilot 3.0.0 or newer | Reports only | Reports, and takes server commands while the switch is on |

"Reports only" means that the server shows what runs here, but cannot run
commands here. Under CashPilot 2.x, Desktop 2.0 still listens on TCP 8086
while the switch is on, but a server that never listed `desktop_commands`
never learns the address. A server that stops listing it, for example after
a downgrade to 2.x, can still get the address in one more heartbeat. The
heartbeat after that leaves it out. The window and the background helper
each send their own heartbeats, so each can send that one. To stop server
commands before a downgrade, turn the switch off first.

- Desktop 2.0 sends its command address only when the server's previous
  heartbeat answer said that it can command a Desktop safely. CashPilot 3.0.0
  sends that answer. An older server never gets the address, except in the one
  heartbeat after a downgrade described above. While the switch is on, the
  dashboard card says that the server is older than CashPilot 3.0.0.
- Desktop 1.7.0 has no server commands. It stays report-only under CashPilot
  3.0.0, so you can upgrade the server first.
- Upgrade the server first, then Desktop.

## Before you upgrade

1. Upgrade your CashPilot server to 3.0.0 or newer. If you only run Desktop
   (Run everything here), skip this step.
2. Remove every `NVIDIA_` variable from your custom services. Desktop 2.0
   refuses an `NVIDIA_` variable when you save a service and when you deploy
   it. A custom service from 1.x that sets one keeps running, but you cannot
   deploy it again until you remove the variable.
3. Move GPU work out of your custom services. In 2.0, a custom service gets
   no GPU. A custom service that uses an NVIDIA GPU through Docker's nvidia
   default runtime runs without the GPU after its next deploy. Run that
   workload outside CashPilot, or ask for a catalog entry.
4. Know what happens if you go back to 1.x. Desktop 2.0 moves this computer's
   pairing with your CashPilot into a new file, `pairing.json`, and deletes the
   old keys. After that, a return to Desktop 1.x needs a new join. When you
   upgrade to 2.0 again, the window applies the choice you made in 1.x when
   it first starts:
   - You joined a server in 1.x, the same one or another one: 2.0 moves that
     pairing over, as on a first upgrade. The keychain must be unlocked and the
     server must answer when the window starts. If not, unlock the keychain or
     restore the connection, then quit CashPilot from the tray and start it
     again.
   - You left the server in 1.x: 2.0 leaves it too.
   - You did not change the pairing in 1.x: 2.0 keeps its own pairing.
   - 1.x has a server address but no key for it: 2.0 shows that address and
     waits. Join again or leave in Settings.

   When the pairing changes, the key that 2.0 held before stops working.
   Server commands start off again, because 1.x drops the saved switch. Turn
   them on with the switch on the dashboard card. Until the window has applied
   the 1.x choice, this computer does not report to any server and takes no
   commands.
5. Download 2.0 from the
   [releases page](https://github.com/GeiserX/CashPilot-Desktop/releases).
   Desktop does not update itself and does not tell you about a new version.

## After you upgrade

These steps apply to a computer that was joined to a CashPilot server in 1.x.

1. Start CashPilot while your server is reachable. When the window starts,
   it moves the pairing to `pairing.json` if the server answers. The
   background helper cannot do it. If the server was down, quit CashPilot
   from the tray and start it again. On a first upgrade from 1.x, this
   computer keeps reporting with its old key until the move. After a return
   to 1.x and back, it does not report until the window applies the 1.x
   choice (step 4 of "Before you upgrade").
2. If the background helper is on, the window restarts it when it opens, so
   the helper runs 2.0 too. Until then, the old helper keeps running and sends
   its own heartbeats, and the server's Fleet page swaps between 1.x and 2.0
   for this computer.
3. Turn on server commands if you want them. They start off after an upgrade
   from 1.x, because an upgrade is not a choice to allow them. Use the switch
   on the dashboard card. A computer that joins for the first time in 2.0
   starts with server commands on, and the same switch turns them off.
4. On Windows, also press **Allow through Windows Firewall** on the same card.
   CashPilot does not listen until Windows Firewall lets it in.

The details are in [Join my CashPilot](configuration.md#join-my-cashpilot).

### If your CashPilot refuses this computer's key

The dashboard then says "Your CashPilot refused this computer's key." This
happens when the server answers HTTP 401, for example after the owner removed
this computer on the server's Fleet page. Desktop 1.x joined again by itself
with the shared key. Desktop 2.0 does not. After the move to `pairing.json`,
it stops the heartbeat and the server commands until you join again by hand.
A pairing that is not moved yet keeps sending the heartbeat with its old key
and shows the same message. In both cases, join again:

1. In Settings, open **How this computer runs** and switch to
   **Run everything here**.
2. Switch back to **Join my CashPilot** and join again.

### Change the server address in Settings

After the move to `pairing.json`, an edit of `upstreamUrl` in `config.json`
does not change the server. Use Settings, or leave and join again.

## Server commands

While this computer is joined and the switch is on, your CashPilot deploys,
stops, starts, restarts and removes services here. It uses the same routes as
on a CashPilot Docker worker.

- CashPilot listens on TCP 8086, on this computer's private network and
  Tailscale addresses only (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16,
  100.64.0.0/10, fc00::/7). It refuses requests from other addresses.
- Only the key that the server gave this computer at the join works. The
  shared key is refused.
- A deploy runs this computer's own catalog entry and pinned image. Every
  local deploy rule still applies.
- The server cannot delete data that cannot be recovered, such as a node
  identity or a wallet.
- The server can use the checked deploy route. That route replaces only the
  container that the owner approved. CashPilot 3.0.0 still sends Desktops down
  the older route, which replaces whatever runs.
- The dashboard card shows which server can send commands and the last command
  that it sent.

The full rules are in
[The server starts and stops services here](configuration.md#the-server-starts-and-stops-services-here).

## GPU miners

A catalog miner gets the GPU that its catalog entry declares, as on a CashPilot
worker. Only this computer's catalog decides that. The deploy form, a joined
CashPilot, a custom service and a proxied copy cannot ask for a GPU.

- Every container without an NVIDIA grant gets
  `NVIDIA_VISIBLE_DEVICES=void`. A CUDA image then cannot take the GPU on a
  host whose default Docker runtime is nvidia.
- No catalog entry, custom service or request may set an `NVIDIA_` variable.
- As a lead, Desktop refuses to send a GPU miner to a worker whose GPU grant
  for that miner differs from this computer's catalog. A worker that reports
  no grants is told to update.
- GPU support is in beta until it runs on a real GPU host.

## New in the catalog

- Pool balances for SupportXMR, HeroMiners Kaspa and 2Miners ETC, read from
  your payout address.
- XMRig, a Monero CPU miner. The deploy card shows its 1% developer fee before
  you deploy. Its Max CPU threads (%) setting caps the threads that it starts,
  on arm64, on x86 and on virtual machines.
- unMineable, which mines with XMRig and pays out in the coin that you pick.
  The balance shows in that coin.
- Dawn and Gradient do not read the referral code from the sign-up link, so
  their cards now show the code to type, with a Copy button.
- SRBMiner-Multi, a GPU miner for Pearl, Zano or Ravencoin on the pool that
  you choose, and KRig, which mines Pearl on Kryptex Pool.

## Fixes that you can notice

- On Windows, a stop, restart, redeploy or remove of a native earner
  (Mysterium) no longer waits 30 seconds before the earner stops.
- Docker actions are off while Docker is stopped, and the page says how to
  fix it.
- As a lead, a Redeploy yes counts only for the worker that it names. If
  another worker takes over the row with the same name, Desktop asks again.
- When you remove a copy that is already gone, Desktop shows the worker's own
  message.

The full list is in the [changelog](../CHANGELOG.md).
