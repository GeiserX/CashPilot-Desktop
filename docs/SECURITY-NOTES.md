# Security Notes — Dependency Scanning

CashPilot-Desktop runs [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) on every
pull request (the `govulncheck` job in `.github/workflows/ci.yml`). `govulncheck` is call-graph aware — it
only flags a vulnerability when the vulnerable *symbol* is actually reachable from our code, not merely
present in the dependency tree.

## Go standard library

The release and CI workflows build with the **latest Go 1.26.x** patch (`go-version: '1.26'` /
`'1.26.x'`), which pulls in each Go security release automatically. Recent stdlib advisories
(`GO-2026-5856` crypto/tls, `GO-2026-5039` net/textproto, `GO-2026-5037` crypto/x509) are fixed in
go1.26.4 / go1.26.5, so the CI runners and the shipped binaries are already patched. If you build locally
with an **older** toolchain you may see these — update your Go toolchain (`go1.26.5` or newer).

## Accepted findings (`github.com/docker/docker` — upstream-unfixed)

The scan is currently **advisory (non-blocking)** because of the findings below. They are all in
`github.com/docker/docker@v28.5.2+incompatible`, all marked **`Fixed in: N/A`** (no fixed release exists
in the Go vulnerability database yet), and they are **Docker Engine / moby daemon** issues that
`govulncheck` surfaces on the client SDK because moby ships client and server code in one module.
CashPilot-Desktop is a **Docker client** — it talks to a Docker daemon over the socket and does not run
the vulnerable daemon code paths. Most reported traces are package-`init` reachability
(`runtime.init → client.init → …`) rather than genuine calls into the vulnerable functions.

| ID | Summary | Why accepted |
|---|---|---|
| [GO-2026-5746](https://pkg.go.dev/vuln/GO-2026-5746) | `PUT /containers/{id}/archive` executes a container binary on the host | Daemon-side; no fixed release; client SDK false-positive |
| [GO-2026-5668](https://pkg.go.dev/vuln/GO-2026-5668) | `docker cp` race — arbitrary empty file creation via symlink swap | Daemon-side; no fixed release |
| [GO-2026-5617](https://pkg.go.dev/vuln/GO-2026-5617) | `docker cp` race — bind-mount redirection to a host path | Daemon-side; no fixed release |
| [GO-2026-4887](https://pkg.go.dev/vuln/GO-2026-4887) | AuthZ plugin bypass on oversized request bodies | Daemon-side; no fixed release |
| [GO-2026-4883](https://pkg.go.dev/vuln/GO-2026-4883) | Off-by-one in plugin privilege validation | Daemon-side; no fixed release |

**The real mitigation for these is on the machine running the Docker Engine** (keep Docker/moby updated),
not in this client app. The exposure of the *daemon* is unchanged whether or not CashPilot-Desktop is
installed.

## When moby ships fixes

When `github.com/docker/docker` publishes releases that resolve the above (the Go vuln DB will then show a
`Fixed in:` version):

1. Bump `github.com/docker/docker` in `go.mod`, run `go mod tidy`, confirm the build + tests pass.
2. Re-run `govulncheck ./...` and confirm the findings clear.
3. Remove `continue-on-error: true` from the `govulncheck` job in `.github/workflows/ci.yml` so the scan
   becomes a **hard gate** again, and delete the accepted list above.

## Reporting a vulnerability

Found something? Please open a private security advisory via the repository's **Security → Report a
vulnerability** tab rather than a public issue, so it can be triaged before disclosure.
