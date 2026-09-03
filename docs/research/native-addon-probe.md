# better-sqlite3 as the native-addon probe

Research for [#835](https://github.com/ocelhq/ocel/issues/835), part of the test-suite map [#830](https://github.com/ocelhq/ocel/issues/830).
Throwaway branch `research/native-addon-probe` — not for merge.

## Verdict

**better-sqlite3 works on both computes, pinned to `13.0.3`, imported through its
platform subpath exports — never through its default entry.**

The default entry (`import Database from "better-sqlite3"`) bundles **clean, with no
warning, and then crashes at runtime on Lambda**. That silent-build/loud-runtime split is
the whole finding: the probe route must name the platform in the specifier.

The premise in the ticket is out of date. better-sqlite3 v13 (2026-07-21) dropped
`prebuild-install` and GitHub release assets entirely. There is no ABI matrix, no install
script, no network fetch. Four of the seven risks the ticket asks about no longer exist.

## The route shape that works

```ts
import linuxArm64 from "better-sqlite3/linux-arm64";
import linuxX64 from "better-sqlite3/linux-x64";

const Database =
  process.platform === "linux"
    ? process.arch === "arm64"
      ? linuxArm64
      : linuxX64
    : (await import("better-sqlite3")).default;

const db = new Database(":memory:");
db.exec("create table probe (n integer)");
db.prepare("insert into probe values (?), (?)").run(1, 2);

// GET /probe/native
{
  sum: db.prepare("select sum(n) as s from probe").get().s,   // 3
  engine: db.prepare("select sqlite_version() as v").get().v, // "3.53.4"
  arch: process.arch,
}
```

The two static imports are what the bundler can see. The dynamic branch is for `ocel dev`
on a developer machine, where the app runs unbundled and the package's own resolution
works; in a bundle that branch is inlined but unreachable.

Assert `arch` in the contract as well as `sum`. A probe that only asserts the query result
passes on the wrong binary; asserting the arch catches an artifact built for the wrong
target, which is the failure this probe exists to catch.

## Why the subpath and not the default entry

`better-sqlite3/linux-x64` is a one-liner whose require is statically analysable
([lib/linux-x64.js](https://github.com/WiseLibs/better-sqlite3/blob/master/lib/linux-x64.js)):

```js
module.exports = require("./database")(() => require("../prebuilds/linux-x64.node"), false);
```

The default entry instead computes the path at runtime
([lib/binding.js](https://raw.githubusercontent.com/WiseLibs/better-sqlite3/master/lib/binding.js)):

```js
const target = `${isLinuxMusl() ? "linuxmusl" : process.platform}-${process.arch}`;
const filename = path.join(__dirname, "..", "prebuilds", `${target}.node`);
if (fs.existsSync(filename)) return filename;
```

esbuild cannot follow that, and ocel rewrites `__dirname` to the bundle's own directory
(`cli/internal/appbundler/appbundler.go:30-35`, `:85`). So on Lambda the lookup resolves
under `/var/task/..`, misses, falls through to the node-gyp locations, and throws.

Reproduced against ocel's esbuild settings (bundle, platform node, ESM, `node24`,
`__dirname` redefined, the `.node` externalize-and-copy plugin):

| Import | Build | Addons copied | Runtime |
| --- | --- | --- | --- |
| `better-sqlite3` | succeeds, zero warnings | none | `Cannot find module '.../build/Release/better_sqlite3.node'` |
| `better-sqlite3/linux-x64` | succeeds | `node_modules/better-sqlite3/prebuilds/linux-x64.node` | works |
| the shape above | succeeds | both linux prebuilds | works, `{"answer":3,"engine":"3.53.4"}` |

Artifact cost: **4.2 MB** for the bundle plus both linux binaries.

## How ocel packages it

The serverless path is `cli/internal/appbundler/appbundler.go:72-92` — esbuild with
`bundle: true`, ESM, **no `external` list and no `packages: "external"`**. Everything
resolvable is inlined into `index.mjs` at the zip root; nothing else reaches
`node_modules`.

`.node` files are the one exception, handled by the `ocel-native-addon` plugin
(`appbundler.go:191-212`): the file is externalized to a relative specifier and copied to
`node_modules/<pkg name>/<rel path>` (`addonDest`, `:280-287`), then resolved at runtime
through the banner's `createRequire`. This lands better-sqlite3's prebuilds at exactly
`node_modules/better-sqlite3/prebuilds/<target>.node`, matching the package's own layout.

A second `OnResolve` (`appbundler.go:205-228`) **fails the build** for any import of
`@mapbox/node-pre-gyp`, `bindings`, `node-gyp-build`, `node-pre-gyp`, or
`prebuild-install`. better-sqlite3 v13 depends on none of them (`node-addon-api` is its
only runtime dependency), so it clears the denylist — verified against the published
tarball. On v12 it would not have.

Containers take a different path entirely: `cli/internal/imagebuild/choice.go:22-48` picks
a configured Dockerfile, a colocated one, or railpack. Dependencies install **inside the
image**, so the prebuilds arrive by plain `npm install` on the target OS and the bundler
never sees them. Nothing in ocel pins the container Node version — railpack reads it from
the app.

## Targets and floors

| | Lambda | Container |
| --- | --- | --- |
| Node | `nodejs24.x` (`cli/node/src/builder/registry.ts:13`, `platform/aws/provider/deploy/function.go:21`) | railpack's choice |
| Arch | **x86_64, not configurable** — the membrane layer pins it (`function.go:37`, `:314-316`, `:539`) | local docker daemon's arch, no platform pin (`imagebuild/build.go:118-153`) |
| Prebuild used | `linux-x64.node` | `linux-x64` or `linux-arm64` |

`engines: node >=22` — no Node 20 leg, which is moot since `nodejs20.x` was deprecated
2026-04-30 ([Lambda runtimes](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtimes.html)).

ABI does not enter into it. The prebuilds are Node-API — one binary per platform, not per
`NODE_MODULE_VERSION`. `nm -D` on `linux-x64.node` shows `napi_register_module_v1`, and
the binary built on Node 24 loads on Node 26 unchanged.

**glibc floor is 2.34, with zero headroom.** From `objdump -T` on both linux prebuilds:
max `GLIBC_2.34`, max `GLIBCXX_3.4.29`. Amazon Linux 2023 ships glibc 2.34
([AL2023 toolchain](https://docs.aws.amazon.com/linux/al2023/ug/glibc-gcc-and-binutils.html));
the live `public.ecr.aws/lambda/nodejs:22` image carries 2.34 and `libstdc++.so.6.0.33` on
both arches. It fits exactly. Upstream builds on `ubuntu-22.04`
([build.yml](https://raw.githubusercontent.com/WiseLibs/better-sqlite3/master/.github/workflows/build.yml)) —
v13.0.3's entire changelog is holding that runner. A bump to 24.04 breaks AL2023 silently
at `require()` time.

For the container leg this rules out Debian bullseye (glibc 2.31, GLIBCXX_3.4.28). Use
bookworm (2.36) or trixie (2.41).

## Writable paths

None needed. `:memory:` is entirely in RAM, so Lambda's `/tmp`-only writability
([ephemeral storage](https://docs.aws.amazon.com/lambda/latest/dg/configuration-ephemeral-storage.html))
never comes up. Verified with the package tree read-only.

If the probe ever grows a file-backed fixture: a read-only WAL database needs write access
to its directory ([SQLite WAL](https://www.sqlite.org/wal.html)), which `/var/task` is
not — ship it `journal_mode=DELETE` or open it `?immutable=1`. Heavy sorts can also spill
to temp files; `SQLITE_TMPDIR=/tmp` covers that.

## Pin and guards

```json
{ "dependencies": { "better-sqlite3": "13.0.3" } }
```

Exact, not `^13`. The glibc floor is a property of the build runner, not of semver.

- **Never `13.0.0` or `13.0.1`** — both carry `install: node-gyp rebuild`. `13.0.2+` has no
  install script and `gypfile: false`, so `npm ci --ignore-scripts` works and no compiler
  or network fetch is involved.
- Gate the floor in CI on every bump:
  `objdump -p node_modules/better-sqlite3/prebuilds/linux-x64.node | grep -oE 'GLIBC_2\.[0-9]+' | sort -uV | tail -1` must stay `<= GLIBC_2.34`.
- Keep the container leg off Alpine. `isLinuxMusl()` picks `linuxmusl-*`, a different
  binary — the arch assertion in the route will not catch that, so it belongs in the
  expectations file rather than the contract.

## Alternatives

Not needed, but for the record: sharp has a much safer glibc floor (2.28) and its own
`@img/sharp-linux-{x64,arm64}` optional dependencies. That mechanism is the problem —
`npm ci --omit=optional`, cross-arch installs, and lockfiles resolved on another platform
all break it, so the probe would be testing package-manager behaviour rather than addon
loading. better-sqlite3 ships one byte-identical tarball everywhere, leaving `dlopen` as
the only variable. That is the signal a native-addon probe should isolate.

## Open, for the spec

Container images are built for the local docker daemon's architecture with no platform
attribute set (`cli/internal/imagebuild/build.go:118-153`) and nothing reconciles that with
the VPS host arch. An arm64 developer deploying to an amd64 box gets an unrunnable image
with no check. The native-addon probe will surface this as a confusing addon failure rather
than as the arch mismatch it is.

## Sources

Primary, all checked directly:
[releases API](https://api.github.com/repos/WiseLibs/better-sqlite3/releases) ·
[package.json](https://raw.githubusercontent.com/WiseLibs/better-sqlite3/master/package.json) ·
[lib/binding.js](https://raw.githubusercontent.com/WiseLibs/better-sqlite3/master/lib/binding.js) ·
[build.yml](https://raw.githubusercontent.com/WiseLibs/better-sqlite3/master/.github/workflows/build.yml) ·
published tarball `better-sqlite3-13.0.3.tgz` (`objdump`, `nm`) ·
`public.ecr.aws/lambda/nodejs:22` image layers, amd64 and arm64 ·
[Node addons](https://nodejs.org/api/addons.html) ·
[abi_version_registry.json](https://raw.githubusercontent.com/nodejs/node/main/doc/abi_version_registry.json) ·
[Lambda runtimes](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtimes.html) ·
[Lambda ephemeral storage](https://docs.aws.amazon.com/lambda/latest/dg/configuration-ephemeral-storage.html) ·
[AL2023 toolchain](https://docs.aws.amazon.com/linux/al2023/ug/glibc-gcc-and-binutils.html) ·
[SQLite WAL](https://www.sqlite.org/wal.html) ·
[sharp install](https://sharp.pixelplumbing.com/install) ·
[Debian libc6](https://packages.debian.org/search?keywords=libc6&searchon=names&suite=all&section=all)
