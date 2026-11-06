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

## What a proxy can be

| Type | What you give it | Notes |
|---|---|---|
| SOCKS5 | host, port, optional user and password | TCP only: the tunnel lets out only TCP to the proxy's port, so SOCKS5 UDP ASSOCIATE relays are dropped. Name lookups still work (see [DNS](#dns)). |
| HTTP CONNECT | host, port, optional user and password | TCP only. Name lookups still work (see [DNS](#dns)). |
| Tailscale exit node | an auth key, the exit node's name or 100.x address, and optionally a login server (for headscale) | Traffic leaves from the exit node's connection. See [Tailscale exit nodes](#tailscale-exit-nodes). |

Any provider works. Nothing in CashPilot is specific to one.

The definition, login and auth key are stored encrypted. The window only
learns whether a secret is set: an edit form leaves those boxes empty, and
leaving one empty keeps what is stored. To drop a stored user or password, tick
the matching **Remove the stored** box and save.

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
proxy is stopped and you get a notice. Every copy recorded for that proxy is
then marked as stopped by the cap, including one you had already stopped
yourself, and a reset starts every marked copy. Stop a copy again after the
reset if you want it to stay stopped.

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
  fleet.
- **The tunnel runs on Docker's default bridge, not on the fenced
  `cashpilot-isolated` network** ([LAN isolation](configuration.md#lan-isolation)).
  The tunnel's own firewall lets out only its connection to the proxy, so the
  earner cannot reach your LAN directly, and a proxy at a LAN or tailnet address
  is reachable here, where the web worker's fence drops it. For SOCKS5 and HTTP
  the tunnel does not filter private destinations inside the tunnel, so the
  earner reaches whatever the proxy itself can reach and allows: a proxy on your
  LAN, or one that connects to private addresses, gives the earner your LAN.
  Use a proxy outside your network that refuses private destinations.
  Tailscale tunnels drop private and tailnet ranges inside the tunnel.
