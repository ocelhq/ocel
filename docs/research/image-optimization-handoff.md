# Image Optimization Stack — Session Handoff

Written 2026-08-03. Read this, then read
`docs/research/image-optimization-design.md` — that document is the live spec and is
authoritative. This file only covers state, process, and the traps that cost time.

## Where things stand

PRs 1–3 of 6 are implemented, reviewed, fixed and committed. **Nothing is pushed and no
GitHub PRs exist yet.** The stack is local only.

```
main (5a63297)
 └── image-opt-manifest-config   b287b2d   PR 1  done
  └── image-opt-s3-mirror        a07afc0   PR 2  done
   └── image-opt-edge-validation 8897344   PR 3  done   <- currently checked out
    └── image-opt-colo-cache               PR 4  not started
     └── image-opt-origin-lambda           PR 5  not started
      └── image-opt-r2-tier                PR 6  not started
```

`gh stack view --json` reports `needsRebase: false` for all three. Tracking lives in bd
under epic `ocelhq-pf6q`, children `.1`–`.6`.

Verification state at handoff:
- `packages/next-runtime`: typecheck, build, 154 tests pass.
- `cloud/aws`: `go build ./...`, `go test -race ./...`, `gofmt -l .` all clean.
- `workers/nextjs`: typecheck clean, 456 tests pass.

## Working agreement for this stack

The user's instruction was: orchestrate, don't implement. Every PR runs
implement → adversarial review → fix → commit, each as a separate subagent, with the
orchestrator holding only the design and the decisions. That has been worth it — the
review pass caught a publicly-served config artifact, an unauthenticated worker crash,
and a middleware-ordering divergence, none of which the implementer saw.

Other standing constraints:
- Never `git add -A`. The tree carries unrelated pre-existing untracked files
  (`.impeccable/`, `apps/web/DESIGN.md`, `docs/adr/`, `manifesto.md`, `*.pen`,
  `examples/next-test/app/basic|handlers`, `args.json`, `docs/research/aws-edge-primitives.md`).
  Stage explicit paths.
- Do not add an AI or agent name as a commit co-author.
- Commit only; do not push or open PRs without asking.
- Decisions belong to the user. Put them in a selectable multiple-choice prompt with a
  recommendation, not free text, and wait.

## What changed in the spec while building

The design doc has been amended five times. If you remember an earlier version, distrust
that memory and re-read it. The substantive changes:

1. Build no longer fails on `images.unoptimized` / non-default `loader` — it warns and
   omits the images section.
2. Asset content hashes cover `public/` too, not just `.next/static`. This was the
   original spec being wrong: `public/` is where `<Image src="/logo.png" />` resolves.
3. `image-config.json` moved to `image-config/<slug>/<app>/<buildID>.json` in the S3
   asset bucket **only**. It was going under `assets/`, which is the app's public web
   root — publicly served, and an exact key collision with a project's own
   `public/image-config.json`.
4. `Vary: Accept` is served-image-only. Next's 400s and 500s carry no `Vary`,
   `Content-Type` or `Cache-Control` — the fixtures record this.
5. A sixth deliberate divergence was registered: malformed Accept parameters.

## Traps that cost real time

- **Next 16 defaults differ sharply from 14/15.** `minimumCacheTTL` 60 → 14400,
  `qualities` undefined → `[75]`, `imageSizes` dropped 16, `localPatterns` defaults to
  `[{pathname:"**", search:""}]` so `/foo.png?v=1` 400s by default. Ocel targets Next 16.
- **Wildcard-only `Accept` negotiates nothing.** Next applies a literal
  `accept.includes(resolved)` guard after resolving the media type, so `*/*` and
  `image/*` fall back to the source format. Undocumented upstream. The fixtures prove it.
- **Next interleaves the `w` and `q` checks.** `?w=0` with no `q` is a *quality* error.
  The spec had this wrong; the fixture generator caught it on first run.
- **`assets/<slug>/<app>/<buildID>/` is the public web root.** The worker serves any
  unmatched path from it (`key = ${assetPrefix}${url.pathname}`). Never put a
  control-plane artifact there.
- **Next applies no `sharp.block()` allowlist and never sets `VIPS_BLOCK_UNTRUSTED`.**
  Do not look for it upstream to copy — PR 5 must add it. CVE-2026-66066, CVSS 9.5.
- **Lambda container images are Region-locked and need every customer account enumerated**
  in the ECR repo policy. That is why PR 5 is a zip the CLI uploads into the customer's
  own bucket, not a container.
- Subagents occasionally misnarrate provenance — one reported PR 3 as "already
  implemented from a prior session" when it had just written every file itself. Verify
  claims independently: check `git status`, file mtimes, and re-run the tests yourself
  before trusting a report.

## Next step: PR 4 — colo cache

`bd show ocelhq-pf6q.4`. Branch off the top of the stack:

```
git checkout image-opt-edge-validation
gh stack add image-opt-colo-cache
```

Scope is the design doc's "PR 4" section. The pieces:

- Wire the image route through the existing colo machinery in `workers/nextjs/src/cache.ts`
  (`serveCached`, `storeInColo`, `refreshOnce`, `evaluate`).
- Cache key exactly as the doc's "Cache key" section specifies, `configHash` included.
- TTL derivation per the doc: `max(minimumCacheTTL, upstreamMaxAge)`, `s-maxage` before
  `max-age`, `Expires` never consulted.
- `CacheStatus` in `cache.ts` gains `STALE` (today it is `HIT|PRERENDER|MISS|BYPASS`).
- Version the entry format so PR 6's R2 tier drops in without a key change or a flush.

Two things PR 3 left for PR 4 specifically:
- `ImageParams.isStatic` currently matches `/_next/static/media` only. The TTL section
  also names `/_next/static/immutable/media`. Widen it when deriving `Cache-Control`.
- The origin stub (`unprovisionedImageOrigin`) returns 502 and is bound nowhere in
  production. PR 4 wraps it in caching; PR 5 replaces the body. `ImageOriginRequest`
  already carries the full eight-field payload, so no caller needs to move.

## Not part of this epic

Two pre-existing defects were found during review and filed standalone. Do not fold them
into this stack.

- `ocelhq-a0wy` (P1) — prune deletes by build prefix with no trailing slash, so pruning
  build `v1` can destroy live build `v10`'s assets, edge bundle and ISR entries. The edge
  bundle is the worst of the three: deleting it breaks the live deployment outright. The
  fix cannot go in `appAssetR2Prefix` (it is baked into the Deployment record and joined
  as `${assetPrefix}${pathname}`) nor inside `deletePrefix` (Reclaim deliberately passes a
  full object key through it). It has to go at the deletion seam.
- `ocelhq-05dy` (P2, `needs-triage`) — skip-if-exists upload serves stale `public/` and
  error-page bytes forever under a repeated build id. Needs a design choice among three
  fixes; that is a human call.

Also outstanding and unrelated: the membrane layer ARN is pinned to us-east-1, so any
customer deploying elsewhere fails on it. The user plans to fix it on the artifact
distribution foundation PR 5 establishes.
