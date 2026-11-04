# Activity: what each earner does on the network

Bandwidth earners carry other people's traffic through your connection. The
Activity page shows what each earner container does with it: how many
connections it opens and how many fail, how many are open at once, which ports
and networks it talks to, the country those networks are registered in, and
whether it tries to reach your own network or send mail.

It is the same feature as the web console's Activity page
(https://github.com/GeiserX/CashPilot/blob/main/docs/activity.md), on one
machine instead of a fleet.

It is **off by default**. Switch it on in the Activity tab. Switching it off
deletes the remote addresses at once.

## What is read, and what is not

Every 5 minutes, while Activity is on, the app starts one short-lived helper
container per earner. The helper reads, from the kernel:

- the socket tables (`/proc/net/tcp`, `tcp6`, `udp`, `udp6`): which local port
  is connected to which remote address and port, and in what state. 5 snapshots
  over 8 seconds.
- the TCP and UDP counters (`/proc/net/snmp`, `/proc/net/snmp6`): connections
  opened, failed attempts, resets, datagrams.

It never reads what is inside the traffic: no packet capture, no names looked
up, no URLs. It never changes the earner container.

The helper's script is the web console's `app/procnet.py`, byte for byte, so
both apps classify a destination the same way.

## Earners on the host network (Mysterium)

An earner with `network_mode: host` shares the machine's socket table, so
reading `/proc/net` would show every socket on the machine. Instead its helper
asks the kernel for the socket list over `sock_diag`, which reports the cgroup
each socket was created in, and keeps only the sockets of the earner's own
cgroup. This needs no extra privilege: the helper joins the host network and the
host's cgroup namespace (to find the earner's cgroup under the read-only
`/sys/fs/cgroup`) and keeps every other limit below. No other socket leaves the
helper.

What that row cannot show, and says so:

- connections already closing (TIME_WAIT) carry no cgroup, so a finished
  connection may be missed; the helper does not even ask for them, so a busy
  host does not run it out of memory;
- when the kernel answers only part of the socket list (one without UDP socket
  listing, for example) or the list passes 200,000 sockets, the row keeps what
  it read and shows a `+` after its count, so it never passes as complete;
- the connection counters are the machine's, so opens and failures show a dash;
- traffic an earner forwards for VPN customers is routed by the kernel, not
  opened by the earner, so it is not shown. Seeing it would need the kernel's
  connection tracking table, which needs `CAP_NET_ADMIN` on the host network or
  root, and the helper gets neither.

When the cgroup cannot be found the row reads "host network: cannot be told
apart" with the reason: cgroup v1 (the machine needs cgroup v2), the cgroup was
not found (rootless Docker or Podman can hide it), the kernel refused the
socket list, or the kernel does not report a socket's cgroup.

## The helper

`python:3.15-alpine`, pinned by digest, pulled once on the first round after you
switch Activity on (about 20 MB). It runs as user 65534, with no capabilities, a
read-only filesystem, no new privileges, 32 processes and 48 MiB at most, a
64 KiB tmpfs at `/data` and no mounts. It carries the labels
`cashpilot.probe=activity` and `cashpilot.probe.worker=<this app>`, and it is
removed on every path. Before each round the app removes any helper of its own
that is left, any finished helper of another app (its log still holds that
round's addresses), and any stopped helper from an older CashPilot that
labelled no worker. It never touches another app's running helper.

Programs run natively (not in a container) show "native program" and are not
sampled.

## Retention

| Data | Kept |
|---|---|
| Remote addresses (the "last 24 hours" list) | 24 hours. Purged on every round and every hour, overwritten on disk (`secure_delete`), then the database log is emptied. |
| Counts per window, per container | 35 days |
| Totals per hour by port, network, country, kind and direction (no addresses) | 35 days |
| Alerts | 35 days |

**Delete data** deletes everything.

## Network names and countries

The app downloads the iptoasn.com `ip2asn-combined` table (about 11 MB,
"Licensed under Public Domain (PDDL v1.0)") into `ipgeo.sqlite` in its data
directory and looks every address up locally. It refreshes it once a week while
Activity is on; the page has a **Download now** button. No address is sent
anywhere.

## Alerts

Alerts appear in the bell. They name the container, the kind of destination or
port, and counts, never an address.

| Alert | When | Repeats at most |
|---|---|---|
| Mail | Outbound TCP to an internet address on port 25, 465, 587 or 2525, in any state. | once a day per container |
| Your network | Any outbound connection to your LAN, your tailnet, the Docker host, a container on its bridge that is not an earner, cloud metadata or a link-local address. | every 6 hours per container and kind |
| Unusual hour | The last hour moved more than 10 times the median hour of the past week and over 1 GiB, or opened more than 10 times the median and over 2,000 connections. Needs 72 hours of history. | every 12 hours |

## Not covered

A Desktop paired with a CashPilot server sends no Activity to it, and a Desktop
running as a hub does not switch Activity on for its remote workers.
