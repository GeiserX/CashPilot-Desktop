# Security Notes — Dependency Scanning

CashPilot-Desktop runs [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) on every
pull request (the `govulncheck` job in [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)). `govulncheck` is call-graph aware — it
only flags a vulnerability when the vulnerable *symbol* is actually reachable from our code, not merely
present in the dependency tree. The job is a hard gate: a finding our code can reach fails the build.

## Go standard library

The release and CI workflows pin an exact toolchain, `go-version: '1.26.8'`, so a release built today and one
rebuilt next month use the same compiler and standard library. The cost of an exact pin is that Go security
releases do not arrive on their own: the workflows sat on `'1.26.5'` while go1.26.6 fixed five advisories our
code actually calls (`GO-2026-6218` net/url, `GO-2026-6090` crypto/tls, `GO-2026-6089` and `GO-2026-5026`
net/http, `GO-2026-5972` encoding/asn1), and those shipped in release binaries until the gate caught it.

The `govulncheck` gate is what keeps the pin honest. It runs against the pinned toolchain, so a stdlib advisory
our code can reach fails CI on the next pull request. When it does, or when a Go security release ships, bump
the pin in all three places (`ci.yml` twice, `desktop-release.yml` once) to the latest 1.26.x and let the gate
confirm it.

If you build locally with an older toolchain you will see those findings. Update your Go toolchain.

## The Docker SDK: `github.com/moby/moby/client`, not `github.com/docker/docker`

We talk to the Docker daemon through [`github.com/moby/moby/client`](https://pkg.go.dev/github.com/moby/moby/client)
and its types module [`github.com/moby/moby/api`](https://pkg.go.dev/github.com/moby/moby/api). Nothing in
this repository imports `github.com/docker/docker`, and nothing should.

`github.com/docker/docker` is a dead end for Go consumers. Its last importable release is
`v28.5.2+incompatible`, from November 2025. Engine 29.x exists, but moby tags those releases
`docker-v29.x.y`, and that is not a semver tag, so the module proxy cannot serve them.
`go get github.com/docker/docker@v29.3.1+incompatible` fails with `unknown revision`. The daemon moved to
`github.com/moby/moby/v2`, which holds no client code at all, and the client split out into
`github.com/moby/moby/client`.

So the late-2025 Engine advisories report no fixed version for `github.com/docker/docker`, and there will
never be one. That covers all five this repository used to carry: the
[archive endpoint host execution](https://pkg.go.dev/vuln/GO-2026-5746), the two `docker cp` races
([symlink swap](https://pkg.go.dev/vuln/GO-2026-5668),
[bind-mount redirection](https://pkg.go.dev/vuln/GO-2026-5617)), the
[AuthZ plugin bypass](https://pkg.go.dev/vuln/GO-2026-4887), and the
[plugin-privilege off-by-one](https://pkg.go.dev/vuln/GO-2026-4883). The fix is the module move, not a
version bump. Any tool that reads `firstPatchedVersion: null` as "unfixable" is wrong here.

The move also shrank the dependency tree. `github.com/docker/docker` ships the daemon and the client in one
module, so every daemon CVE landed on us even though this app only ever acts as a client.
`github.com/moby/moby/client` carries client code alone, which is what makes the scan clean enough to gate
on.

### Working on the Docker calls

The `moby/moby/client` API is options in, result out. `cli.ContainerList(ctx, client.ContainerListOptions{})`
returns a `ContainerListResult` whose `Items` field holds the slice, and create, start, stop, remove and
stats all follow that shape. Three other differences bite when you port code, all of them in
[`internal/runtime/runtime.go`](../internal/runtime/runtime.go):

- Filters are `client.Filters`, built with `make(client.Filters).Add("label", ...)`. There is no `filters`
  package any more. The wire format is unchanged.
- Ports are `network.Port` values parsed by `network.ParsePort`, not raw `nat.Port` strings, so
  `buildPorts` rejects a bad mapping up front instead of handing it to the daemon. It also rejects anything
  that is not exactly `host:container`. Those used to be skipped, which started the container with no port
  published and looked like a clean deploy.
- API-version negotiation is on by default and runs lazily on the first versioned request.
  `WithAPIVersionNegotiation()` still exists, but it is a deprecated no-op.

## Reporting a vulnerability

Found something? Please open a private security advisory via the repository's **Security → Report a
vulnerability** tab rather than a public issue, so it can be triaged before disclosure.
