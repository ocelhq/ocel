# Handoff — ISR thundering-herd remediation

Rolling handoff for epic `ocelhq-wvag`. Update the "Current position" section at the end
of each PR; leave the rest as the standing record.

## Where the spec lives

`bd show ocelhq-wvag` holds the full decision record: verified problem statement, the nine
research corrections that overturned parts of the original plan, fifteen numbered decisions,
standing assumptions, and out-of-scope items. **Read it before touching anything.** The
children `ocelhq-wvag.1` … `.8` each cite the decisions they serve.

Do not re-litigate the research corrections. They were established against Next.js 16.2.10
source, Cloudflare docs and AWS docs, and several of them invert the plan's original
instincts — most importantly that the build manifest fixes the *write* path, not the read
path, and that a shared coordinator DO is Cloudflare's named anti-pattern.

## Stack shape

Eight PRs, each rooted on the previous, first rooted at `main`. Dependency-derived order —
note this deliberately inverts the original 1a→1b→2a→2b sequencing, because PR 6 removes the
DynamoDB fallback and therefore cannot land before the publisher that becomes the sole
guarantor of invalidation.

```
main
 └─ 1  isr-herd/01-cache-api-spike        ocelhq-wvag.1   ✅ code complete
     └─ 2  isr-writer worker + DO          ocelhq-wvag.2
         └─ 3  manifest projection         ocelhq-wvag.3
             └─ 4  streams publisher       ocelhq-wvag.4
                 └─ 5  origin reads snapshot ocelhq-wvag.5
                     └─ 6  get drops BatchGetItem ocelhq-wvag.6
                         └─ 7  edge L0/L1   ocelhq-wvag.7
                             └─ 8  edge L2 lease ocelhq-wvag.8
```

## Working method

Subagent-driven throughout: a fresh agent implements, a second reviews, a third applies
review fixes. The orchestrator does not write code. Each PR gets unit + e2e coverage; the
load/herd harness lands with PR 8 and is then run against the whole stack.

## Current position

**PR 1 (`ocelhq-wvag.1`) — code complete, NOT pushed, issue still open.**

Branch `isr-herd/01-cache-api-spike`, six commits, working tree clean. Additive only:
new package `workers/cache-probe/` plus `docs/research/cloudflare-cache-api-spike.md`.
`workers/nextjs` and all other production code untouched.

Verified: `tsc --noEmit` clean, 38 tests passing across 3 files, `wrangler deploy --dry-run`
succeeds at 2.68 KiB. Regression check after the lockfile change: `workers/deployments-store`
67 tests pass, `workers/nextjs` 523 tests pass.

Reviewed once; ten findings, all fixed. The two that mattered were asymmetric-verdict bugs
that would have produced confident-but-invalid numbers: a single cross-isolate cache hit out
of any number of reads was recorded identically to full sharing, and the TTL phase could
report `evicted-early` for a TTL Cloudflare honored perfectly whenever the cache turned out
isolate-local — precisely the outcome the sentinel phase exists to detect.

### The issue is open on purpose

Its acceptance criterion is *recorded measurements*, and those require a deploy to a
Cloudflare account. Every Results section in the findings doc is marked `UNMEASURED`. No
numbers were invented, estimated or extrapolated — two later PRs are designed off these
figures, so a plausible-looking guess is worse than a blank.

### What you must do to close it

```bash
# 1. add a zone route to workers/cache-probe/wrangler.jsonc (placeholder is commented in):
#    "routes": [{ "pattern": "probe.<your-zone>/*", "zone_name": "<your-zone>" }]
cd workers/cache-probe
pnpm wrangler deploy
node scripts/probe.ts --base https://probe.<your-zone>
# optionally locate a TTL floor:
node scripts/probe.ts --base https://probe.<your-zone> --ttls 1,5,10,30,60
```

Results land in `runs/probe.json` (gitignored) plus a printed summary. Paste the summaries
into the findings doc's Results sections, then close `ocelhq-wvag.1`.

**It must be zone-routed.** `caches.default` is inert on `*.workers.dev`, so a workers.dev
deploy measures nothing and reports it as a working `never-cached` result.

### Reading the results

- Quote `crossIsolateHitRate`, never the bare verdict. A colo is many machines, so partial
  sharing is the expected outcome and the `partially-visible` tier exists for it.
- PR 8 sizes L2 fan-in as the §3 isolate count scaled by `1 − crossIsolateHitRate`, **not**
  by colo count.
- The §3 isolate count is a **lower bound** — connection-to-isolate affinity in Workers is
  undocumented, which is the confound this probe cannot escape.
- Prefer the clock-independent evidence (`maxObservedAgeSeconds`, `observedCacheControl`)
  over the poll bracket when they disagree.
- `never-cached` + `verified: false` means misdeployed; `never-cached` + `verified: true` is
  a genuine TTL finding.

## Findings from PR 1 that affect later work

**`caches.default` is inert on `*.workers.dev`, and this is not just a probe concern.**
`cloud/edge/cloudflare/cloudflare.go` `deployApp` enables the workers.dev subdomain only when
an app declares no domains, otherwise attaching zone routes. So a **domainless Ocel deploy
has no colo cache at all** — the existing tag-clock Cache API front, the image-optimizer colo
tier, and the proposed L1 sentinel are all silent no-ops there.

Two consequences: PR 7's L1 must account for it, and PR 8's load harness must be zone-routed
or it measures the uncached path. This also supersedes the bd memory
`cloudflare-s-cache-api-caches-default-is-a`, which predates the custom-domain→routes switch
and claims every deployment lands on workers.dev.

**If the spike finds the colo cache is isolate-local**, L1 is a no-op and PR 7 should be
reconsidered rather than built as specced; PR 8's lease must then absorb isolate-count
fan-in rather than colo-count. That outcome is design-changing — check the findings doc
before designing PR 8.

## Standing constraints for every PR in this stack

- Nothing is pushed and no PR is created without explicit authorization. Commits are local.
- No backward-compatibility shims or migration paths for existing deploys — out of scope
  by decision.
- Do not touch `entry.cacheControl` persistence or the edge's preference for it over the
  manifest. It is load-bearing for correctness: Next's own `SharedCacheControls` override is
  process-local and non-durable, and the render-error clamp rewrites revalidate windows at
  runtime.
- Correct Next 16.2.10 method names: `updateTags(tags, durations?)` on the plural handler,
  `revalidateTag(tags, durations?)` on the singular. `expireTags` and `receiveExpiredTags`
  do not exist.
- Cloudflare Tiered Cache is deliberately not relied on — `originBlocking` sends a
  SigV4-signed request carrying `x-prerender-revalidate` specifically to bypass caching.

## Next step

Once the spike is deployed and the findings doc carries real numbers, close
`ocelhq-wvag.1`, label `ocelhq-wvag.2` `ready-for-agent`, branch
`isr-herd/02-isr-writer` off `isr-herd/01-cache-api-spike`, and dispatch implementation.

PR 2 is the largest infrastructure step in the stack: a new account-level `workers/isr-writer`
package, its Durable Object, and fresh DO plumbing in the Go deploy path — `buildScriptMultipart`
has no DO or migrations support today, only `buildStoreScriptMultipart` does.
