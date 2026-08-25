# Railpack and BuildKit from the Go CLI

Research for ocelhq/ocel#576 (part of the compute-abstraction map, #570). Sources are the
actual source of `railwayapp/railpack`, `moby/buildkit`, `docker/buildx`, and `moby/moby` on
GitHub — code read at pinned commits, not docs or blog posts. `railwayapp/railpack` read at
`d621a207` (2026-08-25, `HEAD`); `moby/buildkit` at tag `v0.32.2` (`2739b23d`, 2026-08-04);
`docker/buildx` at tag `v0.36.1` (`400a2681`, 2026-08-04); `moby/moby` at `2fea1c60`.
Verified 2026-08-26.

## 1. Railpack as a Go module

- **Single module, no submodules.** `go.mod`: `module github.com/railwayapp/railpack`,
  `go 1.26.3`. `core/`, `cli/`, `cmd/cli/`, and `buildkit/` are all plain packages inside
  that one module — no separate `core`-only module a consumer could pull without the CLI's
  dependency tree. The only other `go.mod` files in the repo are unrelated fixture apps under
  `examples/` (e.g. `examples/go-mod/go.mod`), not part of the railpack module graph.
  Source: https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/go.mod#L1-L3

- **Plan generation is a plain exported Go function, and the CLI calls it directly (no
  shell-out).** `core.GenerateBuildPlan(app *app.App, env *app.Environment, options
  *GenerateBuildPlanOptions) (*BuildResult, error)`, package `github.com/railwayapp/railpack/core`.
  Source: https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/core/core.go#L28-L35
  and #L72. Constructors for its inputs are exported from `github.com/railwayapp/railpack/core/app`:
  `func NewApp(path string) (*App, error)` (`core/app/app.go#L25`) and
  `func FromEnvs(envs []string) (*Environment, error)` (`core/app/environment.go#L23`).

- **CLI trace confirms `core` is not CLI-only logic.** `railpack prepare`
  (`cli.PrepareCommand`, `cli/prepare.go#L38-L42`) calls `GenerateBuildResultForCommand`,
  which builds `app.NewApp`, `app.FromEnvs`, and a `core.GenerateBuildPlanOptions`, then
  calls `core.GenerateBuildPlan(app, env, generateOptions)` directly:
  https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/cli/common.go#L67-L111.
  `cmd/cli/main.go` only wires `urfave/cli` commands onto the `cli` package
  (`cmd/cli/main.go#L38-L44) — no duplicated planning logic lives outside `core`. A
  third-party Go program can `import "github.com/railwayapp/railpack/core"` +
  `"github.com/railwayapp/railpack/core/app"` and call `core.GenerateBuildPlan` to get a
  `*core.BuildResult` (with `.Plan *plan.BuildPlan`) without invoking the `railpack` binary
  or a container at all — this is the whole `prepare`-equivalent.

## 2. Railpack's BuildKit frontend — embeddable in-process, not image-only

- **The gateway-frontend handler is a bare exported function, not container-coupled
  logic.** Package `github.com/railwayapp/railpack/buildkit`, file `frontend.go`, which opens
  with its own comment on the split:
  ```go
  // for platforms: used by `ghcr.io/railwayapp/railpack-frontend` as a buildkit frontend
  // the buildkit library consumes buildkit input and exposes it to us via the client.Client interface
  // note that `frontend` and `build` are completely separate paths
  ```
  https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/buildkit/frontend.go#L1-L3

- Container-only wiring is isolated to `StartFrontend`, which just calls
  `gw.RunFromEnvironment(ctx, Build)` (`gw` = `github.com/moby/buildkit/frontend/gateway/grpcclient`)
  — that's the stdio/env-based gRPC handshake buildkitd sets up when it execs a pulled
  frontend image. `Build` itself, the actual handler, has no stdio/container coupling:
  ```go
  func Build(ctx context.Context, c client.Client) (*client.Result, error)
  ```
  (`client` here is `github.com/moby/buildkit/frontend/gateway/client`).
  https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/buildkit/frontend.go#L43-L54

- **Type match**: BuildKit defines `type BuildFunc func(context.Context, Client) (*Result,
  error)` (`github.com/moby/buildkit/frontend/gateway/client/client.go#L25` at `v0.32.2`).
  `buildkit.Build`'s signature is exactly this type — no adapter needed to hand it to
  anything that wants a `gateway.BuildFunc`.

- **In-process invocation path exists on the client side too.** `(*client.Client).Build`
  (the type returned by `client.New` when dialing buildkitd directly) takes a
  `gateway.BuildFunc` argument directly:
  ```go
  func (c *Client) Build(ctx context.Context, opt SolveOpt, product string,
      buildFunc gateway.BuildFunc, statusChan chan *SolveStatus) (*SolveResponse, error)
  ```
  https://github.com/moby/buildkit/blob/v0.32.2/client/build.go#L17 — it spins up a
  `grpcclient.New(...)` gateway client bound to the *existing* solve session and runs
  `g.Run(ctx, buildFunc)`; no separate frontend-image resolution/pull happens on this path
  (image pulling only happens when `SolveOpt.Frontend` names an image-backed frontend, e.g.
  `gateway.v0` with `source=ghcr.io/railwayapp/railpack-frontend`). So a caller holding a
  `*client.Client` can do `bkClient.Build(ctx, solveOpt, "", railpackbuildkit.Build,
  statusChan)`, linking `github.com/railwayapp/railpack/buildkit` directly into its own
  binary instead of buildkitd pulling `ghcr.io/railwayapp/railpack-frontend`.

- **Caveat**: `buildkit.Build` still reads a `railpack-plan.json` out of the solve's build
  context via `c.Solve`/`ref.ReadFile` (`readRailpackPlan`,
  https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/buildkit/frontend.go#L121-L140)
  — even embedded in-process, it still expects the plan as a file in the local-source mount
  fed to the solve, not as a `*plan.BuildPlan` Go argument. So embedding replaces the
  container-image dependency but not the plan-as-file contract between plan generation and
  frontend execution.

## 3. `github.com/moby/buildkit/client` — daemon address, session, exporters

- **`client.New(ctx, address string, opts ...ClientOpt) (*Client, error)`.** If `address ==
  ""` it falls back to a hardcoded default (`unix:///run/buildkit/buildkitd.sock` on Linux,
  `util/appdefaults`). Source: https://github.com/moby/buildkit/blob/v0.32.2/client/client.go#L44,
  #L119-L121; https://github.com/moby/buildkit/blob/v0.32.2/util/appdefaults/appdefaults_linux.go#L3-L6

- **`BUILDKIT_HOST` is not read by the `client` package at all** — it is purely a `buildctl`
  CLI convention (`cmd/buildctl/main.go#L50-L62` reads `os.Getenv("BUILDKIT_HOST")`, falls
  back to `appdefaults.Address`, sets it as the `--addr` flag default, then hands the
  resolved string straight to `client.New` in `cmd/buildctl/common/common.go#L82`). A
  consumer embedding `client` directly must resolve its own address (env var, flag,
  config) — nothing in the library does it implicitly.

- **Address schemes**: `unix://` and `tcp://` are handled inline by `client.New` (strips the
  `tcp://` prefix for grpc-go's default dialer, `client.go#L143-L147`). Everything else
  (`docker-container://`, `podman-container://`, `nerdctl-container://`, `ssh://`,
  `kube-pod://`, `npipe://`) is a pluggable `client/connhelper` scheme that **must be
  blank-imported** to self-register (`connhelper.Register(scheme, fn)` in each subpackage's
  `init()`) — importing `client` alone does not pull these in.
  `client/connhelper/dockercontainer` literally shells `docker exec -i <container> buildctl
  dial-stdio` via `github.com/docker/cli/cli/connhelper/commandconn` — it requires a
  `buildctl` binary already present *inside* the target container:
  https://github.com/moby/buildkit/blob/v0.32.2/client/connhelper/dockercontainer/dockercontainer.go#L18-L34

- **Session is always required, but `client.Solve`/`client.Build` auto-create one — no
  manual `session.NewSession` call for ordinary use.** `SolveOpt` only exposes attachables
  (`Session []session.Attachable`) plus escape hatches (`SharedSession
  *session.Session`, `SessionPreInitialized bool`). Inside `(c *Client) solve`, if
  `SharedSession` is nil it calls `session.NewSession(ctx, opt.SharedKey)` itself, then wires
  `filesync.NewFSSyncProvider(syncedDirs)` from `LocalMounts` and `s.Allow(a)` for each
  `opt.Session` attachable. Source: https://github.com/moby/buildkit/blob/v0.32.2/client/solve.go#L40-L61,
  #L120-L146. `session.Attachable` itself is `interface { Register(*grpc.Server) }`
  (`session/session.go#L32-L35`).

- **Registry auth** goes through `session.Attachable` too:
  `authprovider.NewDockerAuthProvider(cfg authprovider.DockerAuthProviderConfig)
  session.Attachable`, package `github.com/moby/buildkit/session/auth/authprovider`
  (`authprovider.go#L59`), used in `cmd/buildctl/build.go#L196-L197` and passed into
  `client.SolveOpt{Session: attachable}`.

- **Minimal shape** (buildkit's own `examples/build-using-dockerfile/main.go#L87-L105`):
  ```go
  c, err := client.New(ctx, addr)
  ch := make(chan *client.SolveStatus)
  eg.Go(func() error {
      _, err = c.Solve(ctx, nil, *solveOpt, ch)
      return err
  })
  ```
  No explicit session/auth wiring needed here — relies on `Solve`'s auto-created session.

- **`SolveOpt.Exports []client.ExportEntry`**, fields `Type string`, `Attrs
  map[string]string`, `Output filesync.FileOutputFunc` (tar/oci/docker), `OutputDir string`
  (local/dir-based oci), `OutputStore content.Store`. Source: `client/solve.go#L63-L69`.
  Type constants (`client/exporters.go#L9-L14`): `ExporterImage = "image"`, `ExporterLocal =
  "local"`, `ExporterTar = "tar"`, `ExporterOCI = "oci"`, `ExporterDocker = "docker"`.
  Daemon-side dispatch by string in `worker/base/worker.go#L603-L633` (`oci`/`docker` both
  resolve to `ociexporter.New` with different `Variant`).

## 4. Reaching buildkit through plain `dockerd` vs `docker buildx`

- **dockerd genuinely embeds the same buildkit control API** — `daemon/internal/builder-next`
  registers `controlapi` (the real `moby/buildkit` gRPC control server, same one `client.New`
  targets) directly onto the daemon's own gRPC server:
  ```go
  func (b *Builder) RegisterGRPC(s *grpc.Server) { b.controller.Register(s) }
  ```
  https://github.com/moby/moby/blob/2fea1c60/daemon/internal/builder-next/builder.go#L146-L149

- **But it is not exposed as a standalone buildkitd socket.** It's reachable only through the
  Docker Engine API's HTTP server, via a `/grpc` endpoint explicitly marked deprecated in
  current `moby/moby` source (`daemon/server/router/grpc/grpc.go#L1-L67`: "Clients should
  establish gRPC connections directly over HTTP/2"). Plain `client.New(ctx,
  "unix:///var/run/docker.sock")` **does not work** — that socket doesn't speak raw buildkit
  gRPC framing at the root; reaching it requires an HTTP hijack/upgrade of `/grpc`
  (deprecated) plus a separate hijack of `/session` for session traffic.

- **`DOCKER_BUILDKIT` is a client-side (`docker` CLI) toggle only** — not read anywhere in
  `moby/moby`'s daemon build-routing code; the daemon's buildkit backend is registered
  unconditionally.

- **`docker buildx` has 5 driver types** (`driver/<name>/factory.go` at `docker/buildx@400a2681`,
  registered names): `docker`, `docker-container`, `kubernetes`, `remote`, `cloud`.
  - The **`docker` driver** talks to dockerd's *embedded* buildkit exactly via the hijack
    described above:
    ```go
    func (d *Driver) Dial(ctx context.Context) (net.Conn, error) {
        return d.DockerAPI.DialHijack(ctx, "/grpc", "h2c", d.DialMeta)
    }
    func (d *Driver) Client(ctx context.Context, opts ...client.ClientOpt) (*client.Client, error) {
        opts = append([]client.ClientOpt{
            client.WithContextDialer(func(context.Context, string) (net.Conn, error) { return d.Dial(ctx) }),
            client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
                return d.DockerAPI.DialHijack(ctx, "/session", proto, meta)
            }),
        }, opts...)
        return client.New(ctx, "", opts...)
    }
    ```
    https://github.com/docker/buildx/blob/v0.36.1/driver/docker/driver.go#L62-L75
  - The **`docker-container` driver** instead spins up a *separate* `moby/buildkit:buildx-stable-1`
    container (`driver/bkimage/bkimage.go#L4`) via the Docker Engine API, then dials it with
    a Docker-Engine-API `exec` running `buildctl dial-stdio` inside that container — never
    touching dockerd's embedded builder at all. Source: `driver/docker-container/driver.go#L92-L120`, #L510-L534.

- **Bottom line**: on plain Linux with only `dockerd` (BuildKit-capable, no Desktop, no
  buildx), the embedded buildkit control API exists and is technically reachable, but only by
  replicating buildx's `docker` driver — a custom `client.WithContextDialer` that hijacks
  `/grpc` (deprecated) and `/session` through the Docker Engine API client, not a bare
  `client.New(dockerSocket)` call. Docker Desktop's embedded buildkitd is reached the same
  way (Desktop just ships dockerd with this backend and, on macOS/Windows, buildx
  preconfigured) — same `docker` driver hijack path, no fundamental difference from plain
  Linux dockerd. Going through `docker buildx` (either driver) means depending on the buildx
  CLI/plugin being installed, or vendoring `docker/buildx`'s driver packages directly (both
  are BSD-2-ish Apache-2.0-licensed and Go-importable, since `driver/docker` and
  `driver/docker-container` are ordinary exported packages) rather than reimplementing the
  hijack dialer from scratch.

## 5. Stock `dockerfile.v0` frontend through the same client

- Selected via `SolveOpt.Frontend = "dockerfile.v0"` plus `FrontendAttrs
  map[string]string`. Real snippet, `examples/build-using-dockerfile/main.go#L155-L194`:
  ```go
  frontend := "dockerfile.v0" // TODO: use gateway
  frontendAttrs := map[string]string{"filename": filepath.Base(file)}
  if target := clicontext.String("target"); target != "" { frontendAttrs["target"] = target }
  if clicontext.Bool("no-cache") { frontendAttrs["no-cache"] = "" }
  for _, buildArg := range clicontext.StringSlice("build-arg") {
      kv := strings.SplitN(buildArg, "=", 2)
      frontendAttrs["build-arg:"+kv[0]] = kv[1]
  }
  return &client.SolveOpt{
      Exports: []client.ExportEntry{{
          Type:   "docker",
          Attrs:  map[string]string{"name": clicontext.String("tag")},
          Output: func(_ map[string]string) (io.WriteCloser, error) { return w, nil },
      }},
      LocalMounts: map[string]fsutil.FS{
          "context":    cxtLocalMount,
          "dockerfile": dockerfileLocalMount,
      },
      Frontend:      frontend,
      FrontendAttrs: frontendAttrs,
  }, nil
  ```
  `"context"` and `"dockerfile"` are the fixed `LocalMounts` keys `dockerfile.v0` expects —
  this is the exact same `*client.Client`/`SolveOpt` machinery as any other frontend, only
  `Frontend`/`FrontendAttrs`/`LocalMounts` differ from a Railpack-driven build. No separate
  client type or connection is needed for the explicit-Dockerfile path.

## 6. OCI-layout / tar export requirements

- `oci` and `docker` export types share one implementation
  (`exporter/oci/export.go`, `Variant` field distinguishes them), gated by a boolean
  `tar` attr (default `true`): `keyTar = "tar"`.
  https://github.com/moby/buildkit/blob/v0.32.2/exporter/oci/export.go#L61-L94

- **`tar=true` (default)** writes a single archive stream through
  `filesync.CopyFileWriter(ctx, resp, e.id, caller)` into the client's `ExportEntry.Output`
  (an `io.WriteCloser`) via `archiveexporter.Export(...)` — this is what an OCI-tar or
  docker-tar single-file artifact looks like.
  https://github.com/moby/buildkit/blob/v0.32.2/exporter/oci/export.go#L241-L268

- **`tar=false`** writes blobs through a content store built from the client's
  `ExportEntry.OutputStore`/`OutputDir` instead
  (`sessioncontent.NewCallerStore`/`contentutil.CopyChain`). On the client, when
  `OutputDir` is set (no explicit `OutputStore`), `client.Solve` creates a local
  containerd content store rooted at that directory (`contentlocal.NewStore(ex.OutputDir)`)
  and, after solve completes, writes/updates `index.json` there via
  `client/ociindex.NewStoreIndex(storePath).Put(...)` — producing a real OCI image layout
  directory (`blobs/` + `index.json`), not a stream.
  https://github.com/moby/buildkit/blob/v0.32.2/client/solve.go#L164-L171, #L210-L219, #L424-L438

- `client.Solve` enforces that `Output` (file) and `OutputDir`/`OutputStore` (store) are
  mutually exclusive for `ExporterOCI`/`ExporterDocker`: `"both file and store output is not
  supported by %s exporter"`.

- The separate `tar` exporter type (`ExporterTar`, package at `exporter/tar/export.go`,
  package name `local` internally) produces a plain filesystem tarball of the build result —
  distinct from an OCI/docker image archive; not what an OCI-layout transfer path wants.

- **For a direct-over-SSH transfer path**: `type=oci` with `OutputDir` set (no `Output`,
  `tar=false`) is the shape that yields an on-disk OCI layout directory ready to be
  `scp`/`rsync`'d or streamed over SSH as a directory tree; `type=oci`/`type=docker` with
  `Output` set (`tar` defaults `true`) is the shape that yields a single tar stream a
  consumer can pipe over SSH (e.g. into `skopeo copy oci-archive:...` or `docker load`)
  without ever touching a registry.

## 7. License and versioning risk

- **Railpack**: MIT license, root `LICENSE`
  (https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/LICENSE#L1-L3,
  "Copyright (c) 2025 Railway Corp."). **138 releases** total as of the research date; latest
  tag **v0.37.1** (2026-08-24T18:10:57Z), pre-1.0 (`v0.x`). Cadence: roughly every 1-7 days
  recently (v0.36.0 → v0.36.4 within 11-12 Aug; v0.37.0 on 18 Aug; v0.37.1 on 24 Aug).
  Commit activity is daily (commits as recent as the day of the research clone). No root
  `CHANGELOG.md` (a docs-site changelog exists at `docs/src/content/docs/changelog.md`); GitHub
  Releases carry auto-generated notes with an explicit "Breaking Changes" section per release.
  The two most recent releases with breaking-change notes (`v0.37.0`, `v0.37.1`) both describe
  **build/runtime behavior changes** (Debian 13 + GCC 14 base images; `RAILPACK_DISABLE_CACHES`
  no longer read from host env), not Go function/type renames in `core`/`buildkit`. `RELEASE.md`
  documents only a manual `git tag` + push process
  (https://github.com/railwayapp/railpack/blob/d621a20707b64d896daf28f8a918992487aaa9f9/RELEASE.md)
  — nothing in the repo mechanically enforces semver on the exported Go API surface between
  releases, so pinning a Go dependency on `github.com/railwayapp/railpack` at pre-1.0 carries
  real "we read the diff every bump" risk even though no such break has hit `core`/`buildkit`
  packages yet.

- **BuildKit**: Apache License 2.0, root `LICENSE`
  (https://github.com/moby/buildkit/blob/v0.32.2/LICENSE). `go.mod` requires `go 1.26.3`.
  No explicit Go-API-stability statement anywhere in the repo (no `CONTRIBUTING.md`; the only
  deprecation policy doc, `docs/deprecated.md`, covers *feature* deprecation — "remains in
  BuildKit for at least one stable release" — not Go API compatibility). Latest tag
  **v0.32.2** (2026-08-04) — never reached `v1.0.0`, i.e. also pre-1.0 despite being the far
  more mature/widely-embedded project (it's what `docker build` itself runs on, via
  `docker/buildx`/dockerd). Cadence: roughly one minor release every 4-6 weeks with patch
  releases between (v0.29.0 → v0.30.0 → v0.31.0(+.1/.2) → v0.32.0(+.1/.2) across
  2026-03-31 through 2026-08-04). Internal signals of an evolving API: a live `// TODO:
  refactor to better session syncing` comment on `SolveOpt.SharedSession`
  (`client/solve.go#L40-L61`) and a `//nolint:staticcheck` suppressing a deprecated-API lint
  in `client.New` itself (`client.go#L154-L156`). In practice buildkit's `client` package is
  far more widely consumed externally (docker itself, buildx, depot, earthly, etc.) than
  Railpack's `core`/`buildkit` packages, so its de facto stability bar is higher even without
  a written compatibility promise.

- **docker/buildx** (only relevant if the `docker`/`docker-container` driver path is
  vendored rather than reimplemented): Apache License 2.0, same pre-1.0 profile, latest tag
  v0.36.1 (2026-08-04) at research time.
