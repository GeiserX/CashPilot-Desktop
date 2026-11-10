# catalog-overlay

`services/` is a vendored copy of the [CashPilot web catalog](https://github.com/GeiserX/CashPilot/tree/main/services).
The web repository is the source of truth: it is where a new provider is added, where
a dead one is retired, and where the capabilities and environment a container needs
to actually earn are worked out and verified. Desktop copies it; Desktop does not
edit it.

This directory holds everything Desktop keeps that the web catalog does not. Nothing
else may differ. If you need a change to a service, make it in the web repository and
sync it back.

## Syncing

```bash
make catalog-sync                        # fetch GeiserX/CashPilot@main and rewrite services/
make catalog-check                       # report drift, change nothing
go run ./cmd/catalogsync -src ../CashPilot   # use a local checkout instead of fetching
```

`services/` is then regenerated: every web file is copied byte for byte, the overlay
below is applied, and any file the web catalog no longer ships is deleted. Review the
resulting diff and commit it.

A scheduled weekly workflow (`.github/workflows/catalog-drift.yml`, also runnable on
demand) runs the check and fails loudly when the two have parted. It deliberately
does not run on pull requests: a change in the web repository is not a reason to
block an unrelated Desktop PR.

## The two operations

The sync copies upstream bytes verbatim and then applies exactly two kinds of edit.
Neither can touch a field it was not pointed at, which is the point — a
general-purpose "merge this over that" is how a vendored copy silently acquires
differences nobody declared.

### `image-pins.yml` — digest pins

Desktop pins every live service's image to an immutable `@sha256:` digest;
`TestServiceImagesArePinned` in `internal/catalog` enforces it. The web catalog
mostly carries floating references, so the digest is Desktop's to hold.

The sync replaces the one physical line carrying `docker.image`, located by parsing
the YAML rather than by matching text. It fails when:

- a pin's repository or tag differs from the web entry's image — upstream moved or
  re-tagged it, and holding the old digest would run a retired build that looks
  healthy and earns nothing;
- a live web entry carries a floating image and no pin is declared here;
- a pin exists for a slug that is gone, retired, or already pinned upstream.

One entry keeps its tag somewhere else: `anyone-protocol` carries an untagged
`docker.image` in the web catalog plus the legacy `docker.tag: "0.4.10.2-r1"` key. The guard reads that key as
the entry's tag, so its pin names the tag too
(`drumsergio/cashpilot-anyone:0.4.10.2-r1@sha256:...`). The tag is there so an
upstream re-tag stops the sync instead of leaving Desktop on the previous build while
the entry claims otherwise.

### `append/**` — Desktop-only blocks

A file here is appended verbatim to the vendored file of the same relative path,
after one blank line. When the web catalog has no such file, the overlay file becomes
the entry on its own, which is how a Desktop-only service would be carried.

## Declared deltas, today

| Delta | Where | Why |
| --- | --- | --- |
| 14 image digest pins | `image-pins.yml` | Desktop's digest-pin rule; the web catalog ships floating references. |
| `native:` block on mysterium | `append/bandwidth/mysterium.yml` | Desktop can run a service as a supervised native process with no container runtime. The web app deploys through Docker on a worker and has no such runtime. |
| `native:` schema documentation | `append/_schema.yml` | Documents the field above. |

## Fields Desktop reads differently

| Field | What Desktop does | Why |
| --- | --- | --- |
| `docker.fence_gated` | A deploy is refused unless the earner joins the fenced network (`internal/runtime/lanfence.go`), and the compose export refuses the service. The container gets restart policy `no`, as on the web, and Desktop starts it itself, only right after it has applied the fence: at app start, on every fence check, when isolation is turned on, and from a guard that looks every 30 seconds for a node that is down (`internal/runtime/fence_gated_start.go`). A node whose start Docker refused is left to the five-minute fence check. A stop from the dashboard is recorded and stays a stop until Start or a deploy. A node deployed with `unless-stopped` is switched to `no` in place by the first check; one that is down then with no start error is recorded as stopped. Until that first check has succeeded, the old policy still holds: if the computer restarts before it, or the switch fails, Docker can start the node before the fence is back. A failed switch is shown as the fence's error on the dashboard and is tried again at every check. Turning isolation off stops the node before the fence's drops go, and Start refuses it until isolation is on again. | The web worker starts the node the same way, from its own guard. One difference stays: Desktop is an app, so while neither the window nor its background service runs, nothing starts the node. After a reboot it stays stopped until CashPilot starts, and the dashboard shows it as waiting for the fence. |

## Files Desktop reads beside the services

| File | Why |
| --- | --- |
| `_ratings.yml` | The published grading method. `internal/catalog/rating.go` grades each provider's `rating` block from it, the same way the web app does (`app/ratings.py`). It is embedded in the binary with the service files; without it every provider shows "Not rated yet". |

There are no Desktop-only service entries. `vast-ai` used to be one and now exists
upstream, so it is synced like everything else.
