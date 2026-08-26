# Resident box proxy: Caddy vs Traefik vs kamal-proxy

Sources pinned to the commits read: kamal-proxy `fb0f36c386b4bbcb561c936cc10baf2ae497a656` (v0.9.2), caddy `d6637934e866e34f5676210f8a409b139098f954` (v2.11.4), traefik `f2d0794417e4d06343e6e7c4722143f5b34bee45` (v3.7.11), kamal `eee0083b38661c3707c6b6052cc89e85038a096c`.

## Short answer

**Take kamal-proxy.** It is the only one of the three whose *deploy* is a single blocking call that means "the new container passed health checks, traffic is flipped, and the old container has no in-flight requests left". Caddy and Traefik both flip traffic; neither tells you when the old upstream is drained, so with either of them #586's `stop old` step becomes a guess.

The rest of the ticket's criteria are close enough between the three that they do not overturn this. The one real cost is wildcard TLS: kamal-proxy's ACME is `golang.org/x/crypto/acme/autocert`, which has no DNS-01, so no wildcard certificates. Traefik is the only candidate with wildcards out of the box.

## 1. Dynamic upstream flip

### kamal-proxy — a blocking RPC that gates *and* drains

The whole control surface is a Go `net/rpc` server on a **unix socket**, registered as `kamal-proxy`:

```go
h.rpcListener, err = net.Listen("unix", socketPath)
```
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/commands.go

Socket path is `$KAMAL_PROXY_SOCKET`, else `$XDG_RUNTIME_DIR/kamal-proxy.sock`, else `$TMPDIR/kamal-proxy.sock`.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/config.go

`Deploy` is a synchronous handler. `Router.DeployService` does, in order:

1. `createLoadBalancer` builds the new target list and — unless `--force` — calls `lb.WaitUntilHealthy(deployTimeout)`, returning `ErrorTargetFailedToBecomeHealthy` on timeout and disposing the half-built LB.
2. `installLoadBalancer` takes the router write lock, checks host/path availability, swaps the service's load-balancer pointer, and persists a state snapshot.
3. `replaced.Dispose()` then `replaced.DrainAll(deploymentOptions.DrainTimeout)`.

https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/router.go

The flip itself is a pointer swap under `serviceLock` while request routing reads under `RLock` — atomic with respect to request dispatch. The drain is real bookkeeping, not a sleep: every target keeps an `inflightMap` keyed by `*http.Request`, and `Target.Drain` cancels hijacked (websocket/streaming) requests immediately, waits for the rest to finish or hit the deadline, then cancels the remainder:

```go
func (t *Target) Drain(timeout time.Duration) {
	...
	for _, inflight := range toCancel {
		if inflight.hijacked {
			inflight.cancel(ErrorDraining)
		}
	}
WAIT_FOR_REQUESTS_TO_COMPLETE:
	for req := range toCancel {
		select {
		case <-req.Context().Done():
		case <-deadline:
			break WAIT_FOR_REQUESTS_TO_COMPLETE
		}
	}
```
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/target.go

Defaults: `DefaultDeployTimeout = 30s`, `DefaultDrainTimeout = 30s`.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/service.go

The README states the contract plainly: "The `deploy` command also waits for traffic to drain from the old instance before returning. This means it's safe to remove the old instance as soon as `deploy` returns successfully, without interrupting any in-flight requests." And on failure: "If the instance fails to become healthy within a reasonable time, the `deploy` command will stop the deployment and return a non-zero exit code, allowing deployment scripts to handle the failure appropriately."
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/README.md

That is #586's entire cutover contract expressed as one exit code.

### Caddy — live API, whole-config reload, no drain signal

Config changes go through the admin API (default `localhost:2019`), with `POST /load` and `POST|PUT|PATCH|DELETE /config/[path]` for scoped edits, plus `@id` fields for addressing a node without knowing its array index. Docs: "Configuration changes are lightweight, efficient, and incur zero downtime. If the new config fails for any reason, the old config is rolled back into place without downtime."
https://caddyserver.com/docs/api

Two things the docs do not say, both visible in the source.

**A scoped PATCH is a full reload.** `changeConfig` applies the mutation to the in-memory raw config, re-marshals the *entire* document, and hands it to `unsyncedDecodeAndRun`:

```go
	// the mutation is complete, so encode the entire config as JSON
	newCfg, err := json.Marshal(rawCfg[rawConfigKey])
	...
	err = unsyncedDecodeAndRun(newCfg, true)
```
https://github.com/caddyserver/caddy/blob/d663793/caddy.go

`unsyncedDecodeAndRun` starts the new config's apps, swaps the current context, then stops the old one — start-new-before-stop-old, so the ordering is right. But the blast radius of one app's deploy is every app on the box: all HTTP servers are re-provisioned and the old ones decommissioned.

**Reload does not wait for the old servers to drain.** `App.Stop` fires a goroutine per server calling `http.Server.Shutdown`, waits only for those goroutines to *start*, and then explicitly declines to wait for completion unless the process is exiting:

```go
	// if the process is exiting, we need to block here and wait
	// for the grace periods to complete, otherwise the process will
	// terminate before the servers are finished shutting down; but
	// we don't really need to wait for the grace period to finish
	// if the process isn't exiting
	if caddy.Exiting() {
		finishedShutdown.Wait()
	}
```
https://github.com/caddyserver/caddy/blob/d663793/modules/caddyhttp/app.go

In-flight requests on the old config do finish — `grace_period` defaults to 0, documented as "If zero, the grace period is eternal" — but the reload call returns before they do. There is no endpoint that answers "is the old upstream idle yet". The CLI would have to sleep a fixed grace window before `docker stop`, which is exactly the guess #586 is trying to eliminate.

One mitigation worth naming: `POST|PATCH /config/...` supports `If-Match: "<path> <hash>"` for optimistic concurrency, so two concurrent deploys on one box cannot silently clobber each other's config.
https://github.com/caddyserver/caddy/blob/d663793/caddy.go

### Traefik — hot reload through a provider, throttled and fire-and-forget

Traefik has no imperative "set this upstream" call. Dynamic config arrives through a provider and is hot-swapped; the docs promise routing config "can change and is seamlessly hot-reloaded, without any request interruption or connection loss".
https://doc.traefik.io/traefik/getting-started/configuration-overview/

The swap is an atomic handler pointer replace:

```go
// UpdateHandler safely updates the current http.ServeMux with a new one.
func (h *HTTPHandlerSwitcher) UpdateHandler(newHandler http.Handler) {
	h.handler.Set(newHandler)
}
```
https://github.com/traefik/traefik/blob/f2d0794/pkg/middlewares/handler_switcher.go

Requests already dispatched keep running on the old handler, so they complete — and, as with Caddy, nothing reports when the last one has. Traefik's `graceTimeOut` is for shutting down Traefik itself, not for retiring one backend.

Three plausible push mechanisms, all with a lag:

- **File provider** (`providers.file.directory`, `watch` default `true`) — the CLI scps a file per app and Traefik picks it up via fsnotify. Docs warn against mounting individual files: on rename "the file system notifications will be neither triggered nor caught", so mount the parent directory. https://doc.traefik.io/traefik/reference/install-configuration/providers/others/file/
- **HTTP provider** — Traefik *polls*, `pollInterval` default `5s`. https://doc.traefik.io/traefik/reference/install-configuration/providers/others/http/
- **REST provider** — `PUT /api/providers/rest` with the whole dynamic config. It decodes, pushes onto a channel and returns 200 immediately, so the 200 means "accepted", not "applied". It also requires `providers.rest.insecure` to expose it on the `traefik` entrypoint. https://github.com/traefik/traefik/blob/f2d0794/pkg/provider/rest/rest.go

Every provider is wrapped in a throttle: after a config message is forwarded, the aggregator sleeps `providersThrottleDuration` (default `2s`) before taking the next, keeping only the most recent event in a ring channel.
https://github.com/traefik/traefik/blob/f2d0794/pkg/provider/aggregator/aggregator.go

So a Traefik flip is "write, then wait an unbounded-but-small time, then hope". Also note the **Docker provider is the wrong shape for a flip**: it groups containers by service label, so an old and a new container carrying the same labels become two servers of one load-balanced service — a rolling blend, not a cutover — and it wants the Docker socket, which its own docs call "a security concern: If Traefik is attacked, then the attacker might get access to the underlying host". https://doc.traefik.io/traefik/reference/install-configuration/providers/docker/

## 2. Run form and what bootstrap owes

All three ship an official image, so all three run as `docker run --restart unless-stopped`, matching #586's supervision story with nothing added to the box — no systemd unit, no host package.

**kamal-proxy** — Kamal boots it with exactly that shape:

```ruby
docker :run, "--name", container_name, "--network", "kamal", "--detach",
  "--restart", "unless-stopped",
  "--volume", "kamal-proxy-config:/home/kamal-proxy/.config/kamal-proxy", ...
```
https://github.com/basecamp/kamal/blob/eee0083/lib/kamal/commands/proxy.rb

The image is `ubuntu:noble` plus one static binary, running as a non-root `kamal-proxy` user, `EXPOSE 80 443`, `CMD ["kamal-proxy", "run"]`.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/Dockerfile

Bootstrap owes: a shared docker network (targets are `hostname:port`, so app containers must be resolvable from the proxy — Kamal's `docker network create kamal`), a named volume for `~/.config/kamal-proxy` (state + certs), and ports 80/443 bound. `run` takes only `--http-port`, `--https-port`, `--metrics-port`, `--http3`, `--debug`, each also readable from an env var, so there is no config file to template.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/cmd/run.go

**Caddy** owes: ports 80/443, a volume for `/data` (certs) and `/config`, and a decision about the admin endpoint. Admin defaults to `localhost:2019` inside the container, so the CLI reaches it either by `docker exec … caddy reload` or by binding a unix socket into a host path (`admin.listen` accepts unix sockets; origin enforcement is skipped for unix/fd networks). Config durability needs `caddy run --resume`, which "uses the last loaded configuration that was autosaved", written to `AppConfigDir()/autosave.json`.
https://github.com/caddyserver/caddy/blob/d663793/admin.go
https://github.com/caddyserver/caddy/blob/d663793/storage.go
https://caddyserver.com/docs/command-line

**Traefik** owes the most: a static config file (entrypoints, providers, cert resolvers) that **requires a restart to change**, plus a volume for `acme.json`, plus a watched directory for dynamic config. Adding a provider or an entrypoint later is a proxy restart, so bootstrap has to get the static half right up front or accept restarting the proxy on a box that is serving.

## 3. Active health checks — who gates the flip

This is the sharpest divide, because "the proxy has health checks" and "the proxy gates the cutover" are different features.

**kamal-proxy gates.** Health checks are per-*deployment*, started when the target is created and stopped once it is live. Defaults: `GET /up`, interval `1s`, timeout `5s`, port = target port unless `--health-check-port`, expect `200`. `WaitUntilHealthy` blocks the RPC until the target reports healthy or `--deploy-timeout` expires.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/cmd/deploy.go
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/load_balancer.go

Note the mismatch with #586's agreed definition of up — "any HTTP response on the injected `PORT`". kamal-proxy hardcodes a `200` expectation with no `--health-check-status` flag. Closing that gap means either the CLI keeps probing (below) or a patch upstream.

**Caddy does not gate.** `reverse_proxy` active checks are a steady-state LB concern: `health_uri`, `health_port`, `health_interval` (default `30s`), `health_status` (default `200`), `health_passes`/`health_fails` (default `1`). An unhealthy upstream is skipped for routing. There is no "wait until this upstream is healthy" call — `GET /reverse_proxy/upstreams` reports status, so the CLI would poll it.
https://caddyserver.com/docs/caddyfile/directives/reverse_proxy
https://caddyserver.com/docs/api

**Traefik does not gate either.** Service health checks take `path`, `interval` (default `30s`), `timeout` (default `5s`), `method` (default `GET`), `status`, `port`, `scheme`, `mode`; unhealthy servers leave the rotation. Same shape as Caddy — steady state, not a deploy gate.
https://doc.traefik.io/traefik/reference/routing-configuration/http/load-balancing/service/

With Caddy or Traefik the CLI keeps probe ownership (#589's leaning) and the proxy is a dumb switch. With kamal-proxy the proxy owns the probe by default — which is either a simplification or a duplicated probe, depending on how #589 lands. Worth being explicit: kamal-proxy's `--force` flag skips health checks entirely (`MarkAllHealthy`), so "CLI probes, then flips without re-probing" is a supported mode, and the drain guarantee survives it. That keeps the cutover contract agent-neutral: whoever probes, the flip and drain stay the proxy's job.

## 4. TLS

| | ACME built in | Challenges | Wildcard | On-demand |
| --- | --- | --- | --- | --- |
| kamal-proxy | `x/crypto/acme/autocert` | tls-alpn-01, http-01 | **no** | yes, `--tls-on-demand-url` |
| Caddy | CertMagic | http-01, tls-alpn-01; dns-01 needs a plugin build | with a DNS plugin | yes, `ask` endpoint |
| Traefik | lego | http-01, tls-alpn-01, **dns-01 built in** | **yes** | no (host set is derived from router rules) |

**kamal-proxy** builds an `autocert.Manager` with `Prompt: autocert.AcceptTOS`, a `DirCache` under the ACME cache path scoped by a hash of the ACME settings, and `HostWhitelist(options.Hosts...)` unless an on-demand URL is set.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/service.go

autocert "obtains and refreshes certificates automatically using 'tls-alpn-01' or 'http-01' challenge types", and `HostWhitelist` says "Only exact matches are currently supported. Subdomains, regexp or wildcard will not match." So no DNS-01, so **no wildcard**.
https://pkg.go.dev/golang.org/x/crypto/acme/autocert

On-demand is the well-designed part, and directly relevant to custom domains: `--tls-on-demand-url` may be an absolute URL or a path routed through the service itself, letting the app decide. Kamal-proxy `GET`s it with `?host=<name>` and a matching `Host` header; `200` allows issuance, anything else denies, 2s timeout, status and 256 bytes of body logged. Static certs (`--tls-certificate-path` / `--tls-private-key-path`) cover the Cloudflare-origin-cert case.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/README.md

**Caddy**: "By default, Caddy enables two ACME-compatible CAs: Let's Encrypt and ZeroSSL", with HTTP and TLS-ALPN challenges; on-demand holds the handshake while it issues, gated by an `ask` endpoint. Wildcards work, but "Let's Encrypt requires the DNS challenge to obtain wildcard certificates" and "DNS provider support is a community effort" — a DNS plugin means a **custom Caddy build**, i.e. Ocel would ship and maintain its own proxy image.
https://caddyserver.com/docs/automatic-https

**Traefik**: dns-01 is in the standard binary, "wildcard certificates can only be generated through a DNS-01 challenge", storage defaults to `acme.json`, and domains are derived from `tls.domains` or the router's `Host()` matchers. No on-demand equivalent: an unknown host has no router, so no certificate.
https://doc.traefik.io/traefik/reference/install-configuration/tls/certificate-resolvers/acme/

Given VPS edges are unimplemented today, the honest read is: on-demand-per-customer-domain matters sooner than wildcard, and kamal-proxy is the only candidate whose on-demand hook can defer the decision to the app. Wildcard, if it ever becomes load-bearing, is solvable by provisioning the cert out of band and pointing `--tls-certificate-path` at it.

## 5. Multi-app on one box

**kamal-proxy** is built for it. `--host app1.example.com` scopes a service to a host; empty host is a wildcard; `--path-prefix` splits one host across services. Conflicts are refused rather than silently won: `CheckAvailability` returns `ErrorHostInUse` ("host settings conflict with another service") on install.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/server/router.go

Upstreams are named by **service name**, which is the identity that survives restarts. State is a JSON snapshot at `<data-dir>/kamal-proxy.state`, rewritten by `saveStateSnapshot` on every mutation and read back by `RestoreLastSavedState` at boot:

```go
router := server.NewRouter(globalConfig.StatePath())
router.RestoreLastSavedState()
```
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/internal/cmd/run.go

Put the data dir on a named volume (as Kamal does) and a proxy restart re-registers every app's last-known target with no CLI involvement. `kamal-proxy list` returns host/path/target/tls/state per service — a ready-made reconcile surface.

**Caddy** persists via `--resume` + `autosave.json`; routes are addressed by array index unless you assign `@id`s, so the CLI must maintain a stable naming scheme itself. Host routing is a `host` matcher per route.

**Traefik** persists nothing of its own — the *provider* is the state. With the file provider that is a directory of per-app files the CLI owns, which is actually clean and restart-proof; with the REST provider it is in-memory only and lost on restart, since the REST provider holds one config document that the next `PUT` replaces wholesale.

## 6. Footprint, license, config surface

| | image (amd64, compressed) | base | license | version | first release |
| --- | --- | --- | --- | --- | --- |
| kamal-proxy | 41.2 MB (`latest`/`v0.9.2`) | `ubuntu:noble` | MIT | v0.9.2 | Nov 2024 |
| caddy | 23.9 MB (`2.11.4-alpine`) | alpine | Apache-2.0 | v2.11.4 | 2015 |
| traefik | 55.0 MB (`v3.7.11`) | alpine | MIT | v3.7.11 | 2015 |

Sizes from `hub.docker.com/v2/repositories/<repo>/tags/`. Licenses and versions from the GitHub API.

Dependency surface is a fair proxy for attack surface: kamal-proxy's `go.mod` has seven direct requires (websocket, uuid, prometheus, quic-go, cobra, testify, x/crypto) and no config-language, no plugin system, no template engine.
https://github.com/basecamp/kamal-proxy/blob/fb0f36c/go.mod

**Config surface a CLI can own over SSH:**

- **kamal-proxy** — no config file at all. Everything is `docker exec kamal-proxy kamal-proxy deploy <service> --target host:port [...]`, which is exactly how Kamal drives it: `docker :exec, proxy_container_name, "kamal-proxy", *command`. https://github.com/basecamp/kamal/blob/eee0083/lib/kamal/commands/app/proxy.rb One SSH command, one exit code, no file to render, no partial-write race, no marshalling of somebody else's schema.
- **Caddy** — either render a Caddyfile and `docker exec caddy caddy reload`, or generate JSON and `PATCH` the admin API. JSON is more precise but means Ocel owns a model of Caddy's whole config tree; the Caddyfile is friendlier but means text templating and a full-document rewrite per app.
- **Traefik** — render one YAML/TOML file per app into a watched directory. Simple to write, but Ocel now owns a Traefik schema *and* a static config file whose changes need a restart.

## Against the ticket's criteria

| Criterion | kamal-proxy | Caddy | Traefik |
| --- | --- | --- | --- |
| Flip is atomic | yes (pointer swap under write lock) | yes (start-new, stop-old) | yes (handler switch) |
| Flip is acknowledged | yes (blocking RPC) | reload blocks, drain does not | no (throttled, fire-and-forget) |
| Drain signalled to caller | **yes** | **no** | **no** |
| Blast radius of one deploy | one service | whole config re-provisioned | whole provider document |
| Proxy gates the flip | yes | no | no |
| Container + `--restart unless-stopped` | yes | yes | yes |
| Bootstrap owes | network, volume, ports | volume, ports, admin socket, `--resume` | volume, ports, **static config file** |
| ACME on-demand | yes | yes | no |
| Wildcard | no | plugin build | **yes** |
| Host routing, multi-app | yes, conflicts refused | yes | yes |
| Survives proxy restart | yes (state file) | yes (`--resume`) | provider-dependent |
| CLI surface | one exec, one exit code | file+reload or JSON API | file push |
| License | MIT | Apache-2.0 | MIT |
| Maturity | **pre-1.0, one vendor** | mature | mature |

## Recommendation

**kamal-proxy.**

The cutover in #586 is four steps, and three of them are already someone's problem. Starting a container is docker's. Health-gating is either the CLI's or the proxy's. Flipping traffic every candidate does. The step that has no good answer without proxy cooperation is **knowing when it is safe to stop the old container** — and only kamal-proxy answers it. With Caddy or Traefik, `stop old` becomes "sleep N seconds and hope", which is a correctness compromise (Trust) bought to avoid a dependency, and that trade runs the wrong way against the ordering.

Second reason, and nearly as strong: the config surface. kamal-proxy has none. `docker exec kamal-proxy kamal-proxy deploy web --target abc123:3000 --host app.example.com` is a single SSH command whose exit code is the whole deploy result. Caddy and Traefik both require Ocel to own a model of a foreign config schema and to render it correctly on every deploy, forever, across their version changes. That is real Ops cost for a single-operator project.

Third: Kamal itself walked this path. `Kamal::Commands::Proxy#cleanup_traefik` exists to tear Traefik out of boxes upgraded from Kamal 1 — Basecamp ran Traefik in production for this exact job and replaced it with a purpose-built proxy.
https://github.com/basecamp/kamal/blob/eee0083/lib/kamal/commands/proxy.rb

### What we give up, and how to live with it

- **Pre-1.0, effectively single-vendor.** v0.9.2, first release Nov 2024, MIT, ~1.1k stars, active. Mitigation: it is a 547 KB Go repo with seven direct dependencies; forking it is a real option, not a slogan. Pin the image tag the way Kamal does (`MINIMUM_VERSION` compared against the running container's tag, never a silent replace).
- **No wildcard certificates.** Provision out of band and use `--tls-certificate-path`/`--tls-private-key-path` when it comes up. Custom domains — the case that actually looks imminent — are better served by `--tls-on-demand-url` than by wildcards anyway.
- **Health check expects `200`, not "any HTTP response".** Either narrow #586's definition of up for the box target, or have the CLI probe (per #589) and pass `--force` so kamal-proxy flips without re-probing. The drain guarantee is independent of `--force`, so this stays a clean choice rather than a fork in the design.
- **Targets are `hostname:port`.** Requires app containers on a shared docker network with the proxy. That is one `docker network create` in bootstrap.

### If it is rejected

Prefer **Caddy** over Traefik. Its admin API is a real imperative surface with `If-Match` concurrency control, its reload is transactional with rollback, and nothing in it needs a proxy restart to change. The price is an explicit, documented grace window before `docker stop`, and Ocel owning a JSON config model. Traefik's only decisive win is built-in DNS-01 wildcards, and it pays for that with poll/throttle latency, no acknowledgement, a restart-requiring static config, and a Docker provider whose natural behaviour is load-balancing rather than cutover.
