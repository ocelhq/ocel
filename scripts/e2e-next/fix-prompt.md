# Next.js deploy-adapter e2e fix run

Sibling of `sweep-prompt.md`. That one **finds** bugs — it runs Next.js's
deployment-adapter harness (`NEXT_TEST_MODE=deploy`) against the Ocel Next
adapter, classifies failures and files bd issues. This one **fixes** them: it
takes the open `e2e-next` issues, sorts them into what can actually be fixed,
orders them by their real coupling, and drives one implementer per issue —
each of which must prove its fix against **real deployed infrastructure**
before it is allowed to call the issue done.

A fix is not done because a unit test passes. Every issue in this label set is
a *deploy-adapter* bug: it exists in the seam between a Cloudflare worker and
the AWS Lambdas behind it, and nothing below that seam reproduces it. The
verification loop in Step 5 is the load-bearing part of this document.

## Repos and paths (all absolute, all verified to exist)

- Ocel adapter repo: `/home/vndaba/Dev/ocelhq`
- Next.js repo: `/home/vndaba/Dev/next.js` (branch `canary`) — every `pnpm jest`
  invocation runs from here
- Shared sidecar (prebuilt `ocel` and `@ocel/*` packages):
  `/home/vndaba/Dev/ocelhq-work/sidecar` — **read-only for this run**; see Step 5B
  for why you may need your own, and for the one-time repack it needs.
- Results dir: `/home/vndaba/Dev/ocelhq/.coverage` (already gitignored)
- Last sweep's evidence: `/home/vndaba/Dev/ocelhq/.coverage/report.html`,
  `failures.json`, `_ledger.json`
- Committed known-failure baseline:
  `/home/vndaba/Dev/ocelhq/scripts/e2e-next/baseline-manifest.json` (currently `{}`)
- The `ocel` CLI is **not on PATH**. It runs as
  `node <ADAPTER_DIR>/packages/ocel/bin/run.js <args>`.

If `/home/vndaba/Dev/next.js` is not in your context, stop and tell the user to
run `/add-dir /home/vndaba/Dev/next.js`.

Read `/home/vndaba/Dev/ocelhq/scripts/e2e-next/README.md` and the "Isolation
model" section of `sweep-prompt.md` before reasoning about slugs, previews or
teardown. Read `/home/vndaba/Dev/ocelhq/CLAUDE.md` before touching git.

## Isolation model — the parts that bind a fix run

Restated here because getting these wrong strands real infrastructure:

- Every temp app the harness creates mints its **own Ocel project**, slugged
  `e2e-<GITHUB_RUN_ID>-<8-hex hash of the app dir>`, and deploys into it as a
  single persistent preview. The project namespaces the Pulumi stacks, the
  deployments store, the asset prefixes and the Cloudflare worker scripts and
  routes.
- The slug is **derived, not stored**: `projectSlugForApp` in `lib.mjs` hashes
  `NEXT_TEST_DIR || appDir` with `GITHUB_RUN_ID` (falling back to the literal
  `local`). Same dir + same run id ⇒ same slug. That is what lets you redeploy
  into a staged app over and over.
- `ocel preview rm --name <slug> --yes` resolves the project through the
  `ocel.config.ts` **in its working directory**. You cannot tear a project down
  from an arbitrary cwd — teardown runs from the app dir, or from one holding a
  config declaring the same slug.
- The app inside every project is declared under the constant name `app`.
  Isolation is the slug's job, never the app name's.
- Removing an app's only preview pointer takes its whole project with it. That
  is the intended teardown.

### Hard constraint: the slug must stay short

`MAX_SLUG_LEN` in `lib.mjs` says 52, derived from the 63-char DNS label limit.
**That is not the binding constraint.** The slug appears *twice* in the Pulumi
stack name:

```
<slug>--preview-<slug>--app--<21-char deploy id>
```

Pulumi rejects a stack name over 100 characters, so the real ceiling is
`2*slug + 38 <= 100` ⇒ **slug ≤ 31**. Since the slug is
`e2e-` + run id + `-` + 8 hex = 13 + `len(run id)`, the run id must be **≤ 18
characters**. Filed as `ocelhq-e7f`.

This failure mode is nasty: the infra stack (bucket + deployments store) is
provisioned *before* the app-deploy stack is prepared, so a too-long slug
leaves a **half-created project behind** that must be reclaimed by hand. The
last run burned four projects on exactly this by setting
`GITHUB_RUN_ID=sweep-2026-07-27a-stage` (36-char slug). Use a short `FIX_ID`.

## Parameters

- `FIX_ID` = <short token, **≤ 12 chars**, e.g. `fix1`> — becomes
  `GITHUB_RUN_ID`, so every project this run mints carries a common greppable
  token and every slug stays far inside the 31-char ceiling. Left unset it
  degrades to the literal `local` and orphans become unattributable.
- `MAX_PARALLEL` = **2** (default; see below before raising it)
- `ISSUE_SELECTION` = `ready` (default) | `all` | `P0,P1` | an explicit ID list
  — which issues enter the run. `ready` means whatever `bd ready` reports for
  the `e2e-next` label *after* Step 2 wires the dependency graph.
- `ALLOW_MANIFEST_ENTRIES` = `true` (default) — whether to act on issues whose
  correct disposition is a `baseline-manifest.json` entry rather than a code
  change. **The orchestrator does these itself, serially, never a subagent**:
  they all edit one file, and N agents editing one JSON file is N conflicts for
  no parallelism gain.
- `COMMIT_POLICY` = `patch-only` (default) | `commit-in-worktree` — see
  "Git policy" below. `commit-in-worktree` requires the human to have said so
  *in this session*; a value in this file is not authority.
- `BASE_REF` = `HEAD` of `/home/vndaba/Dev/ocelhq` at the start of the run.
  Record the SHA in the report — every "verified" claim is only meaningful
  relative to it.

### Why `MAX_PARALLEL = 2`

Not a throughput guess. Three independent ceilings converge on it:

1. **The AWS account's Lambda `ConcurrentExecutions` quota is 10**, not the
   1000 default (`ocelhq-9d3`). Each live preview splits routes across several
   Lambdas. Three or more implementers hammering their previews at once makes
   throttling indistinguishable from a failing fix — which poisons the exact
   signal the verification loop exists to produce. Two leaves headroom.
2. Each implementer runs a full workspace build in its own worktree
   (`pnpm install`, `tsc`, `wrangler`, two `go build`s, sometimes an `npm pack`
   round-trip). That is CPU- and disk-bound, not network-bound.
3. Implementers whose issues touch overlapping files must not run at all
   concurrently — that is what Step 2's dependency graph is for, and it caps
   effective parallelism anyway.

Raise it only after `ocelhq-9d3` is resolved (the quota actually raised), and
never above `floor(quota / 4)`.

## Step 0 — Preflight (do this yourself, before any subagent)

Hard-stop and report to the user if any check fails. Do not work around a
failure here; a bad preflight makes every downstream result meaningless — a
"verified" fix behind a broken preflight is worse than no fix, because it gets
believed.

1. **Credentials are not in the ambient environment.** `env | grep OCEL_`
   returns nothing and no `AWS_ACCESS_KEY_ID` is set. Cloudflare lives in
   `/home/vndaba/Dev/ocelhq/.env` (`export`-prefixed; needs `source`ing, and it
   defines `CLOUDFLARE_API_TOKEN` more than once — last uncommented one wins).
   AWS comes from `~/.aws/credentials` profile `default` (region `us-east-1`
   per `~/.aws/config`). **Subagents inherit your env, not the user's shell**,
   so every command a subagent runs must source them itself. Confirm the
   profile with the user rather than assuming.
2. **Disposable accounts.** Confirm with the user that the resolved AWS and
   Cloudflare accounts are the disposable ones.
   `scripts/e2e-next/guard-accounts.sh` exists because this suite provisions
   real infrastructure; it needs `EXPECTED_AWS_ACCOUNT_ID` and
   `EXPECTED_CLOUDFLARE_ACCOUNT_ID`, which are **not** in `.env`. Ask for them,
   then run:
   ```bash
   set -a && . /home/vndaba/Dev/ocelhq/.env && set +a
   EXPECTED_AWS_ACCOUNT_ID=<id> EXPECTED_CLOUDFLARE_ACCOUNT_ID=<id> \
     /home/vndaba/Dev/ocelhq/scripts/e2e-next/guard-accounts.sh
   ```
3. **Proxied wildcard DNS record.** The `OCEL_E2E_PREVIEW_DOMAIN` value
   (`*.ocel.site`) must exist as a Cloudflare DNS record on that zone and be
   **proxied — orange cloud**. Deploys only *verify* this record; they no
   longer create per-hostname DNS. Missing or grey-clouded fails every deploy.
4. **Preview substrate.** `ocel bootstrap --preview` must already have been run
   on the account (account-global, not per-project). `ocel preview up` refuses
   without it. Confirm with the user.
5. **`bd` is reachable.** `bd list --label e2e-next --no-pager` returns issues.
   Note the flag spelling asymmetry that bit the last run: **`bd list` takes
   `--label` (singular); `bd create` takes `--labels` (plural).** `bd show` and
   `bd ready` take **no** `--no-pager` flag at all.
6. **No stranded projects from the last run.** Read
   `.coverage/_ledger.json` and confirm every `teardown` reads `succeeded`. Any
   `stranded` slug is live infrastructure and must be reclaimed (or explicitly
   accepted by the user) before you add more.
7. **Baseline smoke.** Stage one app end to end yourself with the Step 5C
   command against an unmodified `BASE_REF` build, confirm it deploys and
   answers 200, then tear it down. Only then spawn anything. This proves the
   whole staging path works *before* any implementer can misread a broken
   environment as a broken fix.

## Step 1 — Pull the issues and triage them into four buckets

```bash
bd list --label e2e-next --no-pager
bd show <id>            # per issue; no --no-pager on this subcommand
```

The debuggers that filed these did real work — read each issue's diagnosis in
full before deciding. **Not every open issue is a code fix, and two of them are
not adapter defects at all.** Sort every issue into exactly one bucket and
**print the table before spawning anything**:

| bucket | meaning | what happens |
| --- | --- | --- |
| `fix-in-code` | a real adapter defect with an identified surface | gets an implementer subagent (Step 5) |
| `manifest-entry` | the *test* is wrong or Vercel-specific; the adapter is correct | orchestrator edits `baseline-manifest.json` (Step 6); no deploy |
| `needs-human` | resolvable only by an action outside the repo (quota, account, layer republish) | no agent; surfaced in the final report |
| `blocked` | correct fix depends on another open issue landing first | waits; re-evaluated after its blocker closes |

Record the bucket and a one-line reason on each issue so the next run does not
re-derive it:

```bash
bd comment <id> "fix-run <FIX_ID>: bucket=<bucket> — <reason>"
bd label add <id> ready-for-agent      # fix-in-code only
bd label remove <id> needs-triage      # once bucketed
```

### Seed dispositions from sweep `sweep-2026-07-27a`

Accurate as of that run. **Re-derive rather than trusting this table** — issues
close, new ones land, and a diagnosis can be wrong. It is here so you start
from the last run's conclusions instead of rediscovering them.

| issue | P | bucket | why |
| --- | --- | --- | --- |
| `ocelhq-9d3` | P0 | **needs-human** | AWS account Lambda `ConcurrentExecutions` quota is 10. Fixable only by a human raising the limit. Also the single biggest source of false failures — see Step 5F. |
| `ocelhq-9q8` | P0 | fix-in-code | prerendered app-page RSC responses served as `text/html` |
| `ocelhq-hzp` | P0 | fix-in-code | every RSC request carrying `Next-Router-State-Tree` 500s |
| `ocelhq-7og` | P1 | fix-in-code | 307/308 `Location` leaks the Lambda's `https://localhost:<port>` origin |
| `ocelhq-e8z` | P1 | fix-in-code | `next.config.js` redirects not applied |
| `ocelhq-i96` | P1 | fix-in-code | client-supplied `x-middleware-set-cookie` forwarded to the Lambda |
| `ocelhq-sae` | P1 | fix-in-code | metadata static routes: wrong content-type/cache-control |
| `ocelhq-8xo` | P2 | **manifest-entry** | Next only emits `crossOrigin` on the manifest link when `VERCEL_ENV === 'preview'`. The test equates "deploy mode" with "Vercel preview". Ocel does not and should not set `VERCEL_ENV`; the adapter is correct. |
| `ocelhq-bvm` | P2 | fix-in-code | worker forwards unencoded path chars; AWS 400s before invoke |
| `ocelhq-dan` | P2 | **blocked** | marked *re-verify after `ocelhq-hzp`*; may dissolve entirely once hzp lands |
| `ocelhq-e7f` | P2 | fix-in-code | `MAX_SLUG_LEN` (52) exceeds the real Pulumi budget (~31) — scripts-only, no deploy needed |
| `ocelhq-e7w` | P2 | fix-in-code | pages-router routes served as downloads |
| `ocelhq-iud` | P2 | fix-in-code | pages-router dynamic prerender stored under literal bracket key |

Two failures in `failures.json` carry `bdIssue: null` — the Cloudflare RUM
beacon (`/cdn-cgi/rum`) injected into a recorded request list. That is zone
configuration, not an adapter defect. If they recur, they are `manifest-entry`
or "turn RUM off on the zone", not a code fix.

## Step 2 — Wire the dependency graph before scheduling anything

Two issues that touch the same files must never run in parallel implementers.
CLAUDE.md's "Generating issues from a plan/PRD" section already states the rule
this repo uses; apply it to the fix set. Issue **B is blocked by A** when:

- B requires code or infrastructure A introduces;
- B and A modify overlapping files or modules, so concurrent work will produce
  merge conflicts;
- B's requirements depend on a decision or API shape A will establish.

Wire every discovered edge, then let `bd` schedule:

```bash
bd dep add <B> <A>          # B depends on A  (equivalently: bd dep <A> --blocks <B>)
bd dep list <id> ...
bd dep cycles
bd ready                    # what may be spawned now
bd blocked                  # what may not
```

Known couplings from the last run (none of these edges exist in the DB yet —
`bd dep list` reports "no dependencies" for all four as of writing):

- **`ocelhq-e7w` + `ocelhq-iud` are two halves of one mechanism.** e7w is the
  `.html` extension the adapter fails to append when emitting
  `outputs.staticFiles` (`packages/next-runtime/src/next-adapter.mts`); iud is
  the R2 dispatch key the worker builds from the raw request pathname
  (`workers/nextjs/src/assets.ts`). They overlap in both files and the second
  is not fixed by the first. **Do not split them across two agents** — give one
  implementer both issues, or serialize them with a dependency edge.
- **`ocelhq-dan` is explicitly marked "re-verify only after `ocelhq-hzp`"** and
  says *do not work it in parallel*. Wire `bd dep add ocelhq-dan ocelhq-hzp`.
  After hzp closes, re-run dan's originating case before doing any work on it;
  it may need no fix at all.
- **`ocelhq-sae` and `ocelhq-9q8` both concern response content-type** on
  different paths (static metadata routes vs prerendered RSC). Check for file
  overlap in `workers/nextjs/src/assets.ts` / `index.ts` and
  `cloud/aws/deploy/assets.go` before letting them run concurrently.

Anything you spawn in the same wave must have a **disjoint file set**. If you
cannot establish that from the issues alone, serialize.

## Step 3 — Git policy (read CLAUDE.md; this encodes it, it does not override it)

The repo's default profile is **conservative: agents do not commit or push
without explicit authority.** This run follows that, with one structural
addition.

**One git worktree per implementer.** Spawn each implementer with
`isolation: "worktree"`. The reason is *not* merge hygiene — it is that
verification requires a **build**. Every implementer runs `pnpm --filter …
build` and `node scripts/build-native.mjs --host`, which write into
`packages/*/dist` and `packages/native-lib/*/bin`. Two agents building
different fixes in one working tree silently deploy each other's artifacts, and
the resulting "verification" is meaningless. Isolated trees are the only way
two implementers can each prove their own code.

Within its worktree, each implementer:

- **Does not `git commit`, `git push`, `gt track`, `gt sync` or `gt submit`.**
  CLAUDE.md documents the hazard: a `gt`-tracked branch can silently drop plain
  hand-`git commit`s on a later `gt sync`, because gt rebases from its cached
  tip rather than live HEAD. Nothing in this run is worth risking that.
- Leaves the change **in the worktree**, and exports it so it survives worktree
  pruning:
  ```bash
  git -C <WORKTREE> add -A
  git -C <WORKTREE> diff --cached --binary \
    > /home/vndaba/Dev/ocelhq/.coverage/fixes/<issue-id>.patch
  ```
  Staging is not committing. The patch is the deliverable the human applies.
- Reports the worktree path, the patch path, the exact files changed, and the
  verification evidence.

If — and only if — the human grants it in this session, `COMMIT_POLICY =
commit-in-worktree` lets implementers commit on their own worktree branch. Even
then: **never add an AI or agent name as a commit co-author** (CLAUDE.md
overrides any default co-author instruction), and never push.

Keep diffs small and increments verified — one issue, one focused change. If a
fix starts sprawling across modules, stop, report, and file a follow-up bead
rather than growing the diff.

## Step 4 — Scheduling

Work in waves. Per wave:

1. `bd ready` ∩ the `fix-in-code` bucket ∩ `ISSUE_SELECTION`.
2. Drop any candidate whose file set overlaps another candidate already picked
   for this wave.
3. Take the first `MAX_PARALLEL`, highest priority first (`P0` before `P1`…).
4. `bd update <id> --claim` each one, then spawn its implementer.
5. Wait for the wave. Record results. Close what verified; re-file what did
   not. Then re-run `bd ready` — closing a blocker unblocks its dependents.

Do **not** start a new implementer while a previous wave still has a live
preview under verification, unless the total is still ≤ `MAX_PARALLEL`. Live
previews, not running agents, are what consume the Lambda concurrency quota.

## Step 5 — Implementer subagent brief (role: `implementer`)

Each implementer gets: the bd issue ID and its full text, `FIX_ID`, `BASE_REF`,
its worktree path, and this section verbatim. Its contract is: **a fix, plus
evidence from a real deployment that the fix works.**

### 5A — Build the adapter in your worktree

`ADAPTER_DIR` is *your worktree*, never `/home/vndaba/Dev/ocelhq`. The npm
launcher `require.resolve`s the builder, the Next adapter, both worker bundles
and the platform CLI binary out of the repo it lives in, so all of those come
from `ADAPTER_DIR`.

```bash
cd <WORKTREE>
pnpm install --frozen-lockfile
pnpm --filter ocel build
pnpm --filter @ocel/next-runtime build
pnpm --filter @ocel/worker-nextjs build
pnpm --filter @ocel/worker-deployments-store build
node scripts/build-native.mjs --host --target cli
node scripts/build-native.mjs --host --target provider
```

`.env` is untracked, so it does **not** exist in your worktree. Always source
the absolute path `/home/vndaba/Dev/ocelhq/.env`.

### 5B — Decide whether you need your own sidecar

The sidecar is the only thing the harness's temp apps see of Ocel:
`deploy.mjs` symlinks both `<temp-app>/node_modules/ocel` and
`<temp-app>/node_modules/@ocel` at it. Two things resolve from *there*, not from
`ADAPTER_DIR`:

- `ocel` — the generated `ocel.config.ts` imports `ocel/config`, and it is
  bundled and executed from the app dir, so the SDK's built
  `packages/ocel/dist` comes from the sidecar;
- `@ocel/provider-aws-<platform>` — `cli/internal/providerlocator` resolves it
  from the app dir, and it ships **both** the deploy binary (`cloud/aws/cmd/deploy`)
  and the Lambda runtime binary (`cloud/aws/cmd/runtime`).

So: a change under `cloud/aws/**`, `cloud/edge/**` or `packages/ocel/src/**` is
**invisible** to a deploy that uses the shared sidecar. `ocelhq-sae` is a live
example — static-asset content-type is stamped at upload time by
`cloud/aws/deploy/assets.go` (`mime.TypeByExtension`), which is provider code.

Note `packages/ocel` now has two faces and they resolve from opposite places:
its `src/**` is the SDK and travels in the sidecar (rebuild needed), while its
`bin/run.js` launcher is invoked straight out of `ADAPTER_DIR` (no rebuild). The
node builder no longer lives there at all — it is at `cli/platform/**`, esbuilt
into `cli/platform/dist` and embedded in the Go binary at `go generate`, so it
reaches a deploy through the `ADAPTER_DIR` CLI build like the rest of `cli/**`.

**Precondition — the shared sidecar must carry `ocel`.** It was packed before
`@ocel/sdk` folded into the root `ocel` package, so it holds only
`node_modules/{@bufbuild,@connectrpc,@ocel,zod}` and `linkSidecar` hard-fails on
it. Check first, and if `node_modules/ocel` is absent, tell the user to run the
one-time repack in `scripts/e2e-next/README.md` ("Repacking the sidecar") —
do **not** repack it yourself while other runs may be using it:

```bash
test -d /home/vndaba/Dev/ocelhq-work/sidecar/node_modules/ocel \
  || echo "STOP: shared sidecar needs the one-time repack"
```

**Never rebuild `/home/vndaba/Dev/ocelhq-work/sidecar`.** A concurrent
implementer's deploy would silently pick up your code. Build your own:

```bash
SIDECAR=/home/vndaba/Dev/ocelhq-work/sidecar-<ISSUE_ID>
TARBALLS=$(mktemp -d)
mkdir -p "$SIDECAR"
cd <WORKTREE>
for pkg in ocel @ocel/provider-aws @ocel/provider-aws-linux-x64; do
  pnpm --filter "$pkg" exec pnpm pack --pack-destination "$TARBALLS"
done
cd "$SIDECAR" && npm init -y >/dev/null && npm install --no-audit --no-fund "$TARBALLS"/*.tgz
test -x node_modules/@ocel/provider-aws-linux-x64/bin/deploy
```

If your change is confined to `workers/nextjs/**`, `packages/next-runtime/**`,
`packages/ocel/bin/**`, `cli/**` (which includes the node builder at
`cli/platform/**`) or `scripts/e2e-next/**`, use the shared sidecar read-only
and skip this.

### 5C — Stage one live app (a single deploy that stays up)

Pick the **narrowest** failing case from your issue and run the harness for
that one test. This is the cheapest way to get a live preview *and* an intact
app directory: one deploy instead of two.

```bash
cd /home/vndaba/Dev/next.js && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
AWS_PROFILE=default \
AWS_REGION=us-east-1 \
ADAPTER_DIR="<WORKTREE>" \
GITHUB_RUN_ID="<FIX_ID>" \
OCEL_ACCESS_TOKEN=thisdoesntmatter \
OCEL_API_URL=https://ocel.app \
OCEL_E2E_PREVIEW_DOMAIN="*.ocel.site" \
OCEL_E2E_SIDECAR_DIR="<SIDECAR>" \
OCEL_E2E_DEPLOY_TIMEOUT_MS=540000 \
HEADLESS=true \
IS_TURBOPACK_TEST=1 \
NEXT_TEST_JOB=1 \
NEXT_TEST_MODE=deploy \
NEXT_E2E_TEST_TIMEOUT=600000 \
NEXT_TELEMETRY_DISABLED=1 \
NEXT_TEST_SKIP_CLEANUP=1 \
NEXT_TEST_DEPLOY_SCRIPT_PATH=<WORKTREE>/scripts/e2e-next/deploy.mjs \
NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH=<WORKTREE>/scripts/e2e-next/logs.mjs \
pnpm jest --runInBand <SUITE_PATH> -t "<FULL JEST TEST NAME>" \
  2>&1 | tee /home/vndaba/Dev/ocelhq/.coverage/_fix-<ISSUE_ID>-stage.log
```

Notes on this block, so you do not "fix" it:

- **`NEXT_TEST_CLEANUP_SCRIPT_PATH` is deliberately absent.** With it set, the
  harness tears the deployment down the moment the test finishes and you have
  nothing to iterate against. Leaving it unset means `cleanup.mjs` never runs —
  which also means **nothing will ever tear this project down but you**. 5G is
  not optional.
- **`NEXT_TEST_SKIP_CLEANUP=1` keeps the temp app directory.** Without it the
  harness deletes it (`test/lib/use-temp-dir.ts`, `test/lib/next-modes/base.ts`)
  and you lose the `ocel.config.ts` that `preview rm` needs.
- `OCEL_E2E_PROJECT_SLUG` is deliberately absent — removed in `3f6034a`; no
  script reads it.
- `OCEL_ACCESS_TOKEN` / `OCEL_API_URL` are inert here (`ocel preview up` makes
  no control-plane call). Kept to match CI.
- `OCEL_E2E_DEPLOY_TIMEOUT_MS` sits under Jest's `NEXT_E2E_TEST_TIMEOUT` so a
  hung deploy fails as a deploy, not as a test.
- `HEADLESS=true` matches upstream CI (`run-tests.js` sets it; invoking `pnpm jest`
  directly does not). Without it `itHeaded` cases run, which upstream always skips,
  so your reproduction would not match the sweep it came from.
- The lifecycle script paths point at **your worktree**, not the main repo, so
  a fix under `scripts/e2e-next/**` (e.g. `ocelhq-e7f`) is actually exercised.

Capture from the run:

- the slug and app dir, printed to stderr as
  `[ocel-e2e] project <slug> in <appDir>`;
- the preview URL, from `<APP_DIR>/.ocel/deploy-result.json` → `appUrls[0]`.

Report both to the orchestrator **immediately**, before you start iterating, so
a killed agent still leaves a reclaimable slug behind.

### 5D — Iterate against the live preview

Edit code in the worktree, rebuild only what changed, redeploy in place:

```bash
cd <WORKTREE> && pnpm --filter @ocel/worker-nextjs build     # or next-runtime / ocel
# Go changes: node scripts/build-native.mjs --host --target cli|provider
#   (and rebuild your sidecar if the change was under cloud/**)

cd <APP_DIR> && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
AWS_PROFILE=default AWS_REGION=us-east-1 \
OCEL_E2E_PREVIEW_DOMAIN="*.ocel.site" \
node <WORKTREE>/packages/ocel/bin/run.js build && \
node <WORKTREE>/packages/ocel/bin/run.js preview up --name <SLUG> --prebuilt
```

`--name` is required: an ephemeral preview resolves its identity from a git
ref, and the temp app directory is not a repo. `--prebuilt` ships the
`.ocel/output` the preceding `ocel build` produced rather than building twice.

You can **skip `ocel build`** when the change cannot affect build output —
worker-only (`workers/nextjs/**`) or provider-only (`cloud/aws/**`) changes go
straight to `preview up --prebuilt`, which is much faster. Changes that feed the
build output require it: the node builder (`cli/platform/**`, which also needs
`go generate` via `build-native.mjs --target cli`), the Next adapter
(`packages/next-runtime/**`), and the SDK (`packages/ocel/src/**`, which the
builder traces into the app's function bundles).

Then re-check the failing route with `curl`, always capturing headers:

```bash
curl -sS -D /tmp/h.txt -o /tmp/b.txt "https://<preview-host><path>"
cat /tmp/h.txt
```

### 5E — The warm-PoP trap (content-type and static-asset fixes)

`serveStaticAsset` (`workers/nextjs/src/assets.ts`) answers from the Cloudflare
colo Cache API before it ever reads R2:

```ts
const cached = await deps.cache.match(request);
if (cached) return cached;
...
"content-type": object.httpMetadata?.contentType || contentTypeFor(url.pathname),
```

Two consequences that have already cost debugging budget:

1. **The PoP cache is keyed on the request URL**, which does not change when
   you redeploy. A redeploy alone does **not** bust it. Vary the URL to force a
   miss — `?v=$(date +%s)` works, because the R2 key is built from
   `url.pathname` only, so the query string reaches the cache key and not the
   object lookup.
2. **`httpMetadata.contentType` is stamped at R2 upload time** by
   `cloud/aws/deploy/assets.go`. `contentTypeFor` in the worker is only the
   fallback for legacy objects. A worker-only content-type change will appear
   to do nothing against existing objects even on a cache miss — the real fix
   is likely provider-side, which puts you in 5B's sidecar case.

`curl -H 'Cache-Control: no-cache'` does **not** bypass the Cache API. If a
cache-busted request still shows the old behaviour, tear down and re-stage from
scratch before concluding your fix failed.

### 5F — Distinguish "my fix didn't work" from "I got throttled"

**This is mandatory before you report any negative result.** The account's
Lambda `ConcurrentExecutions` quota is **10**, not the 1000 default
(`ocelhq-9d3`). Bursts of cache-bypassing requests throttle with
`429 TooManyRequestsException` and surface at the edge as **502 with no
`x-amzn-*` headers and no CloudWatch invocation** — which looks exactly like a
broken fix.

The test:

```bash
# 1. Did the request reach a Lambda at all?
curl -sS -D /tmp/h.txt -o /dev/null "https://<preview-host><path>"
grep -i '^x-amzn-' /tmp/h.txt        # x-amzn-requestid / x-amzn-trace-id
```

No `x-amzn-requestid` on a 5xx ⇒ the request never reached your function.
Confirm against CloudWatch — find the Lambdas **by tag**, filtering on
`ocel:project`, **never** `ocel:app` (which is the constant `app` for every
test app, so it matches every concurrently-deployed app and mixes their logs):

```bash
aws resourcegroupstaggingapi get-resources \
  --tag-filters Key=ocel:project,Values=<SLUG> \
  --resource-type-filters lambda:function

aws logs filter-log-events \
  --log-group-name /aws/lambda/<function-name> \
  --start-time <epoch-ms just before your request> --limit 50 \
  | grep 'START RequestId'
```

**No `START RequestId` at that timestamp ⇒ you were throttled, not broken.**
Back off: serialise your requests, `sleep 2` between them, and never run a full
Jest suite while another implementer is verifying. Current quota:

```bash
aws service-quotas get-service-quota --service-code lambda --quota-code L-B99A9384
```

If throttling is blocking your verification rather than merely confusing it,
stop and report it — do not paper over it with retries.

### 5G — Prove it, then tear down

**Proof** (all three; a fix without all three is not verified):

1. The originating Jest case(s) pass through the harness against your build.
   Re-run the 5C command with `-t` covering every case the issue lists, or
   without `-t` for the whole suite if it is small. Paste the Jest output.
2. The raw HTTP evidence: the `curl` request and the full response headers,
   before and after, with `x-amzn-requestid` present on both.
3. The repo's own tests for every package you touched, e.g.
   `pnpm --filter @ocel/worker-nextjs test`,
   `pnpm --filter @ocel/next-runtime test`,
   `pnpm --filter @ocel-scripts/e2e-next test`, `go test ./...` in the module.

**Membrane-layer caveat.** The Lambda membrane layer is **pinned** in
`cloud/aws/deploy/function.go` (`defaultMembraneLayerARN`). Changes to the
cache handler or the lambdanode bootstrap are **not exercised** by a normal
deploy. If your fix lives there, either republish the layer (see `ocelhq-qc1`)
and point at it with `OCEL_MEMBRANE_LAYER_ARN=<arn>`, or report that your fix
is unverifiable in this run and say so plainly. Do not claim verification you
did not get.

**Teardown — always, even on failure:**

```bash
cd <APP_DIR> && \
set -a && . /home/vndaba/Dev/ocelhq/.env && set +a && \
AWS_PROFILE=default AWS_REGION=us-east-1 \
node <WORKTREE>/packages/ocel/bin/run.js preview rm --name <SLUG> --yes
```

This must run **from the app directory** — `preview rm` addresses the project
through the `ocel.config.ts` in its cwd, so it cannot be run from an arbitrary
directory. Do not delete the app dir until teardown succeeds. If it fails,
leave the directory in place, report the slug as `stranded` with the exact
reclaim command, and say so loudly: that project's Lambdas, worker scripts,
deployments store and DNS label are still live and nothing else will collect
them.

Also remove your per-issue sidecar (`rm -rf /home/vndaba/Dev/ocelhq-work/sidecar-<ISSUE_ID>`)
once teardown is done.

### 5H — Report

Do **not** close the bd issue yourself. Post the evidence and hand the decision
to the orchestrator:

```bash
bd comment <ISSUE_ID> "fix-run <FIX_ID> @ <BASE_REF>: <one-line outcome>
files: <changed files>
patch: /home/vndaba/Dev/ocelhq/.coverage/fixes/<ISSUE_ID>.patch
preview: <url>   slug: <slug>   teardown: succeeded|stranded
verification: <jest result> / <curl before-after> / <unit tests>"
```

Final message to the orchestrator, exactly: the issue ID, `verified` /
`unverified` / `blocked`, the changed file list, the patch path, the slug and
its teardown state, and — if unverified — what you ruled out. Budget: stop
after ~45 minutes of iteration and report `unverified` with your findings
rather than burning the wave.

## Step 6 — Manifest-entry issues (orchestrator does these, serially)

Only when `ALLOW_MANIFEST_ENTRIES = true`. These are cases where the *test* is
wrong for a non-Vercel adapter, so the correct outcome is a baseline entry, not
a code change. `ocelhq-8xo` is the canonical example: Next emits `crossOrigin`
on the manifest link only under `VERCEL_ENV === 'preview'`, and the test
equates deploy mode with Vercel preview. Ocel does not set `VERCEL_ENV`, so
`undefined` is correct and `getAttribute()` returning `null` is correct.

Edit **`scripts/e2e-next/baseline-manifest.json`** — the committed source of
truth. Do **not** edit `nextjs/test/ocel-deploy-tests-manifest.json`; that is
the workflow's copy inside the Next.js checkout, and only the copy step knows
about it. The file is currently `{}`. Per-suite shape:

```json
{
  "test/e2e/app-dir/metadata/metadata.test.ts": {
    "passed": [],
    "failed": ["app dir - metadata basic should support other basic tags"],
    "flakey": [],
    "runtimeError": false
  }
}
```

Case names are the full `ancestors > title` path as Jest reports it, matched
exactly. The manifest only ever *excludes* what it lists, so newly added cases
are still included automatically. Never re-record a whole baseline to add one
entry — that silently adopts every other new failure alongside it.

This file is a tracked, committed source of truth, so the conservative commit
policy applies: leave the edit in the tree and report it. Do not commit it
unless the human says so.

## Step 7 — Report back

Write `/home/vndaba/Dev/ocelhq/.coverage/fix-report.html` — self-contained
(inline CSS, no external assets), openable directly in a browser:

- Headline: `FIX_ID`, `BASE_REF` SHA, issues considered, and counts per bucket
  (`fix-in-code` / `manifest-entry` / `needs-human` / `blocked`), plus
  verified / unverified / still-open.
- The Step 1 triage table with each issue's bucket and reason.
- The dependency edges you added, and why.
- Per fixed issue: the changed file list, the patch path, the preview URL and
  slug, the Jest before/after, and the raw `curl` header evidence.
- Per unverified issue: what was tried and what was ruled out.
- **Needs-human** section, with the exact action required — e.g. "raise AWS
  Lambda `ConcurrentExecutions` from 10 (quota `L-B99A9384`, region
  `us-east-1`)", "republish the membrane layer per `ocelhq-qc1`".
- **Stranded projects** section listing any slug whose teardown failed, each
  with its exact `cd <appDir> && ocel preview rm --name <slug> --yes` command.

And `/home/vndaba/Dev/ocelhq/.coverage/fixes.json`:

```json
{
  "generatedAt": "<ISO8601>",
  "fixId": "<FIX_ID>",
  "baseRef": "<sha>",
  "maxParallel": 2,
  "commitPolicy": "patch-only",
  "issues": [
    {
      "id": "ocelhq-xxx",
      "bucket": "fix-in-code | manifest-entry | needs-human | blocked",
      "outcome": "verified | unverified | manifest | deferred",
      "files": [],
      "patch": "/home/vndaba/Dev/ocelhq/.coverage/fixes/ocelhq-xxx.patch",
      "worktree": "",
      "projectSlug": "",
      "teardown": "succeeded | stranded",
      "notes": "<one line>"
    }
  ],
  "strandedProjects": [],
  "needsHuman": [{ "id": "ocelhq-9d3", "action": "<exact action>" }]
}
```

Then update bd — issue state is the durable record; the HTML is a convenience:

```bash
bd close <id> --reason "fix verified in fix-run <FIX_ID>; patch at .coverage/fixes/<id>.patch"
bd update <id> --add-label needs-human --remove-label ready-for-agent   # human-blocked
bd update <id> --append-notes "..."                                     # unverified
```

Close only what a subagent actually verified against a live deployment. An
issue whose fix is sitting unapplied in a worktree is **not** closed — leave it
open, labelled `ready-for-human`, with the patch path in its notes.

File a new bd issue for anything the run turned up that is not covered by an
existing one. Beads is the tracker: do **not** use TodoWrite or markdown TODO
lists.

Your final message to the user: what was fixed and verified, what is in the
tree awaiting review (with patch paths), what is still open and why, and the
exact human actions blocking the rest.

## Appendix — traps that already cost budget

- **`workers/nextjs/routing-manifest.json` in the working tree is a red
  herring.** It is a scratch artifact for a different app (`appName:
  next-cache-lab`), not the deployed manifest. The real one is in the app's
  `.ocel/output/apps/app/`. Two debuggers wasted budget on it last run.
- **`bd list --label` is singular; `bd create --labels` is plural.** `bd show`
  and `bd ready` reject `--no-pager`.
- **Filter Lambdas by `ocel:project`, never `ocel:app`.** `ocel:app` is the
  constant `app` for every test app and will mix concurrently-deployed apps'
  logs into your diagnosis.
- **A run id longer than 18 characters breaks every deploy** with
  `a stack name cannot exceed 100 characters` — *after* the infra stack is
  provisioned, leaving a half-created project behind.
- **The shared sidecar is read-only.** Rebuilding it mid-run silently changes
  what a concurrent implementer deploys.
- **`preview rm` only works from a directory whose `ocel.config.ts` declares
  the slug.** There is no global "list all my projects" call; a lost app dir
  plus a lost slug is unreclaimable infrastructure. Related open work:
  `ocelhq-e8i` (orphan project sweep).
- **502 with no `x-amzn-*` header is a throttle, not a bug** — until CloudWatch
  says otherwise.
