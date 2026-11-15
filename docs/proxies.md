# Proxied copies

A proxied copy is an extra copy of a service that leaves through a proxy
instead of this computer's own internet connection. Providers count devices per
public IP, so one computer can earn on several IPs: its own, plus one per proxy.

You add a proxy once under **Fleet**, **Proxies**, then pick **Run a copy
through this proxy** and a service already deployed here. The copy runs with
the same settings as that service, under its own device name (`-p3` is added
for proxy 3). Copies run on this computer only.

This is the same feature as the CashPilot web app's
([proxied copies](https://geiserx.github.io/CashPilot/proxies/)), with the
differences listed in [Where Desktop differs](#where-desktop-differs).

A service can also run only through a proxy, from its first start: see
[Route a service at deploy](#route-a-service-at-deploy).

## Route a service at deploy

A proxied copy runs beside the service, so the service itself still starts once
from this computer's own IP. If the provider must never see that IP, pick a
**Route** on the service's setup card. The choices are this computer's own IP
(the default) and each of your proxies. With a proxy, the service runs only inside
its tunnel, from its first start.

- Only services that can run through a proxy get the choice, by the rules in
  [What cannot run through a proxy](#what-cannot-run-through-a-proxy). The
  card says why when a service cannot. Custom services are deployed first and
  then get a copy through the proxy.
- It runs with the settings you deploy it with, under the device name you give
  it. No second device is registered.
- It runs once, in a tunnel of its own. To route a service that already runs
  from this computer's own IP, remove it first, then deploy it with the route.
- Stop, Start, Restart, Logs and Remove act on it. It is listed on the Proxies
  page with the proxy it runs through.
- The app remembers every service routed here, also after you remove it. A
  later deploy from this computer's own IP asks you first, removes the routed
  copy, and then runs the service directly.

Routing hides this computer's IP from the service's own traffic. It does not
hide it from the provider's website, your earnings login or a referral page
you open in the browser: those come from wherever they run.

Start the app with `CASHPILOT_PROXIED_ONLY=true` in its environment and it
refuses every service that does not run through a proxy: a deploy, and a Start
or Restart of a service that runs from this computer's own IP. A service that
was already running that way is stopped when the app starts and checked again
every 30 seconds; Docker would otherwise start it after each reboot. Until the
app is up after a reboot it can run for a moment, so remove such services (or
deploy them again with a route) rather than leave them stopped. The app itself
cannot change this setting.

## What a proxy can be

| Type | What you give it | Notes |
|---|---|---|
| SOCKS5 | host, port, optional user and password | TCP only: the tunnel lets out only TCP to the proxy's port, so SOCKS5 UDP ASSOCIATE relays are dropped. Name lookups still work (see [DNS](#dns)). |
| HTTP CONNECT | host, port, optional user and password | TCP only. Name lookups still work (see [DNS](#dns)). |
| Tailscale exit node | an auth key, the exit node's name or 100.x address, and optionally a login server (for headscale) | Traffic leaves from the exit node's connection. See [Tailscale exit nodes](#tailscale-exit-nodes). |

Any provider works. Nothing in CashPilot is specific to one.

SOCKS5 and HTTP CONNECT send the proxy user and password in the clear, so
anyone on the path between this computer and the proxy can read them and use
your proxy account. Use a proxy reached over a network you trust, or a
Tailscale exit node, whose traffic is encrypted end to end.

The definition, login and auth key are stored encrypted. The window only
learns whether a secret is set: an edit form leaves those boxes empty, and
leaving one empty keeps what is stored. To drop a stored user or password, tick
the matching **Remove the stored** box and save.

A stored secret is kept only while the proxy points at the same server. When
you change a SOCKS5 or HTTP proxy's host or port, or a Tailscale proxy's login
server, type the user, password or auth key again (or tick **Remove the stored**
to save without it); the save is refused otherwise. So an edit can never send a
stored credential to a server it was not entered for. A new name, cap or exit
node keeps the stored secret.

**Test** starts a throwaway tunnel, reports the exit IP and country it gets,
and removes it. A failed test stays on the proxy, with the reason, until the
next test.

Use the proxy user and password your provider gives, not your dashboard login.
Many residential providers rotate the exit IP on every connection unless you
ask for a sticky session, usually inside the user name (for example
`user-session-abc123`). If you leave rotation on, tick **Rotating exit IP**,
because the exit IP shown is sampled once when the tunnel starts.

## How it works

Each copy is two containers:

1. A **tunnel** from `drumsergio/cashpilot-tunnel`, pinned by digest
   (`CASHPILOT_TUNNEL_IMAGE` can point at a mirror, with an exact tag or digest
   only). It owns a network namespace and makes the proxy the only way out.
2. The **earner**, started in the tunnel's network namespace. It has no network
   interface of its own.

The tunnel loads its firewall first: every outgoing packet is dropped unless it
goes into the tunnel or is the tunnel's own connection to the proxy (for
Tailscale, a packet tailscaled marked as its own). Then it starts
[tun2proxy](https://github.com/tun2proxy/tun2proxy) for SOCKS5 and HTTP, or
tailscaled for Tailscale, and probes an exit through the tunnel. The earner
starts only after the probe sees an exit IP within 45 seconds, and, when the
app knows this computer's public IP, one that is not this computer's. Otherwise
the copy is refused and the tunnel removed.

### It fails closed

If the proxy goes down, the tunnel process dies or the tunnel container stops,
the earner cannot send a packet anywhere: not to the internet from your own IP,
not to your LAN. It stops earning; it never leaks. The web repository's
`scripts/proxy_leak_test.sh` and `scripts/proxy_leak_test_tailscale.sh` prove
this for the tunnel image against a real Docker daemon.

After a reboot or a runtime restart, the app starts the earner only once the
tunnel has written its ready file, which happens after the firewall is in and
an exit was seen.

### Hardening

The tunnel gets `NET_ADMIN` and `/dev/net/tun` and nothing else: every other
capability dropped, never privileged, `no-new-privileges`, a read-only root
filesystem, a process limit, no Docker socket and no published ports. Its log
is capped at two files of 1 MB, and tun2proxy logs at `warn`, so it holds errors
and never the sites the earner reaches. The proxy URL or auth key reaches the
tunnel as files, never as a label or an environment variable.

The earner keeps the hardening every deploy gets, and a proxied earner never
holds `NET_ADMIN`, `NET_RAW` or `SYS_ADMIN`, a host device or privileged mode,
since inside the tunnel's namespace those would let it change or bypass the
firewall.

### LAN isolation

With [LAN isolation](configuration.md#lan-isolation) on, the tunnel joins the
fenced `cashpilot-isolated` network, like every other bridge earner, so a
SOCKS5 or HTTP proxy at a LAN or tailnet address cannot be reached: use one on
the internet. A Tailscale exit node still works by its 100.x address, because
tailscaled reaches it through the node's public endpoints, not that address. With
isolation off the tunnel runs on Docker's default bridge, and its own firewall
is what keeps the earner off your LAN. A tunnel created before v0.39.1 ran on
the default bridge in every mode, and stays there until its copy is deployed
again.

For SOCKS5 and HTTP the tunnel does not filter private destinations inside the
tunnel, so the earner reaches whatever the proxy itself can reach and allows. A
proxy that connects to private addresses on its side gives the earner that
network. Use a proxy that refuses private destinations. Tailscale tunnels drop
private and tailnet ranges inside the tunnel.

## DNS

Name lookups from the earner never leave this computer directly. For SOCKS5
and HTTP, tun2proxy answers them inside the tunnel and passes the name to the
proxy when a connection opens, so the proxy resolves it where it is. This works
with plain HTTP CONNECT proxies, which cannot carry DNS themselves. A Tailscale
tunnel runs no tun2proxy: lookups sent to Docker's resolver are redirected to
1.1.1.1 and travel through the exit node like any other packet.

## What cannot run through a proxy

A service is refused, with the reason, when its catalog entry needs:

- host networking, or another container's network;
- published ports;
- volumes or host directories;
- devices (Mysterium needs `/dev/net/tun` and UDP);
- privileged mode, or the capabilities `NET_ADMIN`, `NET_RAW` or `SYS_ADMIN`
  (Bitping asks for `NET_RAW`);
- terms that forbid proxies or VPNs: `requirements.proxy_prohibited` in the
  catalog, with a link to the provider's own terms.

With today's catalog that rules out EarnApp, URnetwork, ProxyBase Markets,
Bitping, Mysterium, Storj, Sia and Anyone Protocol. Services that need UDP do
not work through SOCKS5 or HTTP CONNECT; a Tailscale exit node carries UDP.

## Editing a proxy

A saved edit (a new password, port, type, auth key or exit node) reaches a copy
the next time the app starts it. Every start by the app, by hand or after a
cap reset, writes the proxy as stored into the tunnel and resolves its host
again. A restart Docker does on its own, after a reboot or a tunnel crash,
keeps the settings the tunnel already has. A tunnel still
running on the old settings is stopped with its earner and comes back on the
new ones. To apply an edit to a running copy now, press **Stop**, then
**Start**. If the host no longer resolves, the start stops there, rather than
sending the login to the address it had before.

## Exit IPs

The exit IP a copy shows is the one the tunnel saw when it came up. When a new
copy leaves from an IP where the same service already runs here, or through
another of your proxies, the app warns. If the provider allows one device per
IP, the warning says the copy earns nothing there. Some providers treat a
changing IP as abuse; use sticky sessions where your proxy provider offers them.

## Traffic cap

Optional, off by default. Set a cap in GB (10^9 bytes), the percentage to stop
at, and a reset day (1 to 28) or none for **Reset usage** by hand.

Traffic is counted from the tunnel container's own interface, which every byte
of the earner crosses, including TCP, TLS and WireGuard overhead, so it can
differ from your provider's count. At the threshold every copy through that
proxy is stopped and you get a notice. Only the copies that were running are
marked as stopped by the cap, so a copy you had already stopped stays stopped
after the reset. While the proxy is capped, Start is disabled on its copies:
reset the usage or raise the cap first. A reset starts every marked copy; one
whose start fails keeps its mark, the failure shows once in its events, and the
app tries again every minute until it starts, or until you start or stop it by
hand.

## Tailscale exit nodes

The tunnel runs tailscaled in kernel mode, so a missing exit node means no
traffic, not a leak from your own IP. It also keeps the earner off the tailnet
itself: packets to `100.64.0.0/10`, `fd7a:115c:a1e0::/48` and private ranges are
dropped before they reach tailscaled.

Before you add one:

1. In your tailnet policy, declare a tag in `tagOwners` (for example
   `"tag:cashpilot-proxy": ["autogroup:admin"]`) and allow it to use the exit
   node, for example a grant from `tag:cashpilot-proxy` to `autogroup:internet`.
   Remove or narrow any other rule that matches the tag (a default allow-all
   policy does). The tunnel drops tailnet traffic anyway, but the policy should
   not offer it.
2. Create an auth key that is **reusable**, **ephemeral** and tagged with that
   tag. It must be reusable: the tunnel keeps no state, so every Test, copy and
   restart joins as a new ephemeral node with the same key. If your tailnet
   has device approval on, make the key pre-approved, or every new node waits
   for approval and the tunnel never becomes ready.
3. On the exit node's machine, turn on **Run as exit node**, and approve it in
   the admin console.
4. Add the proxy with the key and the exit node's name or 100.x address. For a
   headscale server, fill in **Login server**.

Keys expire (90 days at most); when one does, every start fails with the key
refused. Create a new key and paste it into the proxy's settings. A leaked
reusable key lets anyone join your tailnet as that tag, which is why it must be
ephemeral, tagged and used for nothing else.

## Where Desktop differs

- **The cap is counted only while the app runs.** The app reads the tunnels'
  counters and checks the cap once a minute, from the window or the tray. While
  it is closed nothing is checked or stopped, so copies can run past the cap.
  When it comes back, the traffic since the last reading is counted, except
  traffic from a tunnel that restarted in the meantime (after a reboot, for
  example), which is lost.
- **The same-exit warning covers this computer only.** It compares the new
  copy's exit IP with this computer's own IP and with the copies through your
  other proxies here. It does not see the services on other machines in your
  fleet, and it skips copies through a proxy ticked **Rotating exit IP**,
  whose sampled IP says nothing about the next connection.
