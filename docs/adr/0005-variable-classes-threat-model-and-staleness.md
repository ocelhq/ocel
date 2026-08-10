# 5. Three variable classes, what each one is actually worth, and how stale a live value gets

Date: 2026-08-01

## Status

Accepted

## Context

`defineEnv` takes a `class` per variable, and that one knob decides two things
at once: how confidential the value is kept, and by what mechanism it reaches
the running process. Those two consequences are not independent choices a user
can mix, and the second one silently sets a third — whether rotating the value
needs a redeploy.

Nothing in either repo stated the resulting table, and the encrypted class in
particular invites a stronger reading than it deserves. This ADR records the
table, states the threat model without overselling it, and pins the one number
a live value's freshness rests on.

Vocabulary (variable, class, cell, folder, env class, store, reference,
required-cell matrix, coordinate manifest) lives in `CONTEXT.md`; this file is
the reasoning.

## Decision

### There are exactly three classes, and no `live` class

The wire enum is `VARIABLE_CLASS_PLAIN = 1`, `VARIABLE_CLASS_SENSITIVE = 2`,
`VARIABLE_CLASS_SECRET = 3` (`proto/resources/v1/env.proto:14-26`); the SDK
spells them `"plain" | "sensitive" | "secret"`
(`packages/ocel/src/env/definition.ts:4`). Prose across this project calls the
third one *live*, because it is the only one fetched at runtime rather than
carried by an artifact — but **there is no `live` class**. `secret` *is* it:
`LIVE_CLASSES = new Set(["secret"])`
(`packages/ocel/src/env/definition.ts:58`), and `isLive` is the single question
the read path asks about a class (`definition.ts:64-66`).

| | `plain` | `sensitive` (encrypted-baked) | `secret` (the live class) |
|---|---|---|---|
| **Confidentiality** | None. Legible in a function-configuration listing. | Ciphertext at rest in the artifact; plaintext only inside the running process. | Never in an artifact or a function configuration at all. |
| **Delivery** | A plaintext entry in the Lambda's environment under the bare key the user chose (`platform/aws/provider/deploy/vars.go:84-96`, `variableEnv`). | AES-256-GCM ciphertext at `.ocel/variables.enc` inside every one of the app's deployment packages (`platform/aws/provider/vars/baked/baked.go:20-23`); the per-deploy data key is the one plaintext configuration entry, `OCEL_VARS_ENVELOPE` (`baked.go:25-29`); the Go membrane opens the file and re-injects each value under `OCEL_VAR_<KEY>` (`baked.go:31-34`, read by the SDK at `packages/ocel/src/env/index.ts:145,169`). | The deploy pins **coordinates only** into `.ocel/variables.live.json` (`platform/aws/provider/vars/live/live.go:26-56`); the membrane reads them, fetches plaintext from DynamoDB+KMS through `vars.Store.Reveal` (`platform/aws/provider/cmd/lambdanode/bootstrap/live.go:58-74`) and pushes it to node over the one-way control socket as a `liveValues` message (`live.go:76-90`). |
| **Rotation cost** | A redeploy. The value is function configuration, written at deploy. | A redeploy. A new value means a new seal — a fresh nonce and fresh ciphertext on every `Seal` (`baked.go:41-59`) — so it is a new artifact by construction. | **None.** Rotating changes nothing in the package, which is precisely why the package holds an address and not a value (`platform/aws/provider/vars/live/live.go:5-10`). Picked up within the staleness bound below. |
| **Who can read the plaintext** | Anyone who can read the function's configuration: the console, `GetFunctionConfiguration`, or a log line that dumps `process.env`. | Anyone who can read the function's configuration **and** download its deployment package. On Lambda that is one API surface, not two — see the threat model. | Only the function's own execution role: `kms:Decrypt` on its class's key, plus a `dynamodb:Query` conditioned on that project's partition and the partitions its own live values are referenced out of (`platform/aws/provider/deploy/vars.go`, `varsReadPolicy`). No artifact and no configuration ever carries it. |

**`client` is not a fourth class.** It is an orthogonal boolean that only
`plain` may carry (`definition.ts:31-43`, `proto/resources/v1/env.proto:36-38`).
Combining it with an encrypted class is a compile-time type error and, for a
caller the compiler never saw, a runtime `EnvDefinitionError`
(`definition.ts:114-119`).

**Footnote — no class reaches the edge tier.** None of the three delivery
mechanisms exists in an edge worker: `plain` needs a Lambda's
`Environment.Variables`, `sensitive` needs the Go membrane to open the sealed
file, and `secret` needs the membrane to query DynamoDB+KMS and push over the
control socket. An edge worker's only bindings are the edge reader's IAM
credentials. `ocel/env` therefore ships an edge build (selected by the
`edge-light` / `workerd` / `worker` export conditions) whose Proxy throws
`EnvEdgeError` on read (`packages/ocel/src/env/edge.ts:39-59`); declaring stays
harmless, only reading throws. **The only remedy is moving the entry to the
`nodejs` runtime — reclassifying buys nothing**, because no class is
deliverable there. Real edge delivery is bd `ocelhq-xd5j.37`.

### The threat model, stated plainly

This repo is not public. The following is the honest version; do not soften it
here, and treat any condensed user-facing wording as a separate decision.

**`sensitive` is not two-factor separation.** Against an attacker who can
already read a function, it buys nothing: the same API surface that returns the
environment (and therefore `OCEL_VARS_ENVELOPE`, the data key) also returns a
link to the deployment package holding the ciphertext. One caller with
`lambda:GetFunction` gets both halves. The encryption is real; the *separation*
is not.

What `sensitive` genuinely protects against is narrower and still worth having:

- configuration-only viewers — a console screen, a screenshot, a support
  session, an IaC diff, or a tool that lists function configuration and never
  downloads packages;
- environment dumps in logs and error reporters, which see `OCEL_VARS_ENVELOPE`
  and no values;
- casual exposure generally — the value is not sitting in plain sight next to
  `NODE_ENV`.

**`secret` is the only class with a real trust boundary.** Its plaintext exists
in exactly two places: the store, and the memory of a running function whose
execution role was granted a `kms:Decrypt` and a partition-scoped
`dynamodb:Query` (`platform/aws/provider/deploy/vars.go`, `varsReadPolicy`). The scope is
the project's own partition plus the partition of every project that function's
own live values are referenced out of, because a reference is followed where it
is read — see the amendment
below. It never reaches a build host — the wire carries presence without
plaintext for a live cell (`proto/resources/v1/env.proto:72-76`) — so a
compromised CI job, an artifact store, and a function-configuration reader all
learn nothing but the key's address.

**`plain` claims nothing.** It exists for interop with code that reads
`process.env` itself, which is the property that distinguishes it
(`platform/aws/provider/deploy/vars.go:75-83`).

The practical rule: choose `secret` when the value's disclosure matters, and
`sensitive` when you want it off the configuration surface but can accept that
anyone who can fetch the artifact can open it.

### The staleness bound: 60 seconds, plus the next invocation

`liveStalenessBound = 60 * time.Second`
(`platform/aws/provider/cmd/lambdanode/bootstrap/live.go:37`) — one bound for the whole
project, because rotation latency is a project-wide operational property. It is
**not user-configurable**: doing so needs a project-config settings surface that
does not exist and a wire field to carry it, so changing it today is a source
edit and a redeploy (`live.go:21-31`).

Three qualifications, all of which matter more than the number:

1. **The clock is read when an invocation arrives, not on a timer.** Lambda
   freezes the sandbox between invocations, so `refreshIfStale` is called on the
   forward path (`platform/aws/provider/cmd/lambdanode/bootstrap/forward.go:31`) rather than
   from a ticker (`live.go:32-36`). An idle warm sandbox holds a value well past
   60 seconds and refreshes on the first work it is given. The bound is
   therefore **"60 seconds plus the next invocation"**, not a wall-clock
   guarantee.
2. **The bound is per property read, not per resolution.** A read is memoised
   against the live generation (`packages/ocel/src/env/index.ts:60,79-90`), so
   `const k = env.SECRET` captured at module scope copies the string out and is
   frozen for the sandbox's life. Only code that re-touches `env.SECRET` ever
   observes a later generation.
3. **A refresh never blocks a request.** `refreshIfStale` returns immediately
   and the previous generation goes on being served while the new one is fetched
   (`live.go:223-263`); an in-flight refresh is not started again, because a
   frozen-then-thawed sandbox can leave one outstanding across a long gap.

**A store outage degrades at request time, not at init time — but only once a
first generation exists.** The two paths differ deliberately:

- **Cold start:** the prefetch runs concurrently with node's boot
  (`live.go:146-171`), and node's join point is the last moment an init failure
  can still be reported as one (`live.go:195-205`). A failed prefetch closes
  `failed` (`live.go:101-106,159-165`) and stops the function coming up with a
  diagnosable error rather than serving a variable that reads as unset. The
  3-second fetch budget exists to leave room for that error
  (`live.go:39-45`).
- **Warm refresh:** a failure is fire-and-forget. It logs
  `"ocel: live value refresh failed, serving the last resolved generation"` to
  stderr and keeps serving the previous generation (`live.go:244-257`). No
  request in flight, and no request after it until a refresh succeeds, ever sees
  an error. The degradation is silent to the application and visible only in
  stderr.

Generations are monotonic from 1 and node ignores any message that does not
advance them, so an out-of-order refresh can never resurrect an older value
(`live.go:80-88`).

**Do not confuse this with the resolve cache.** `cli/internal/resolvecache`
caches the CLI's whole-project *resolve* response — every declared resource, not
variables specifically — keyed by a hash of the resource definitions plus an
account fingerprint, and invalidated by a change to either or by the
server-provided `ExpiresAt` (`cli/internal/resolvecache/resolvecache.go:20-33`,
`107-126`). It governs whether `ocel dev` calls the resolve API at all. It has
nothing to do with how stale a live-class value is once resolved.

**Nor with the declaration cache.** `cli/internal/declcache` holds the
`VariableDefinition` set a discovery run produced — key, class, required, folder
scope — so `ocel env set` need not re-run the pass per write. It holds **scope
metadata and never a plaintext value**; values are the store's half and never
reach it. It is invalidated by one thing: a change to `sha256` of the bundled
discovery program, which esbuild inlines every source file and transitive
dependency into, so any edit to the declaring code moves it. There is **no time
bound** — a fingerprint that covers every input a declaration can come from is
not made truer by expiring. The env class is deliberately not in the
fingerprint: a declaration is what the code states, so preview and production
writes share one entry.

What the fingerprint cannot see is the ambient state the discovery program reads
while it runs — `if (process.env.X) defineEnv(...)` is missing from a cached set
with the code byte-identical. **The write guard therefore never trusts the
cache's absences.** `Cache.Load` takes the key it is being asked about and
reports a hit only for a set that declares it (`declcache.go:66-95`), so a key
the cached set is silent about forces a real discovery run before
`envgate.CheckWritable` decides. Silence is not an answer: reading it as
"unscoped" is exactly how a root cell for a scoped key — an Out of Scope
construct for this epic — would get written, silently. The cost falls only on
writing a key nothing declares, which is either a typo or the stale case being
guarded.

### The two override axes are orthogonal

A value is addressed by a **cell** (key + folder) and, separately, by an **env
class** with optional named-environment overrides beneath it. The axes never
interact.

- **Root → folder.** Resolution for one app is exactly two hops: the app's bound
  folder, then the project root (`cli/internal/envgate/folder.go:46-56`,
  `Resolve`). Nesting never participates — a folder is matched whole, never as a
  path prefix, and the store's key layout enforces that with a terminator
  (`platform/aws/provider/vars/keys.go:122-128`). A root cell is the fallback for every app;
  a folder cell is read only by the app bound there
  (`cli/internal/envgate/envgate.go:293-296`). A key scoped to folders the app
  does not bind is absent from the result rather than resolved from somewhere
  else.
- **Class-wide → named environment.** A named-environment row is a value, not a
  requirement: the gate holds class-wide cells and named-environment overrides
  apart, and the class-wide set is the only one every run sees. **The amendment
  below narrows this**: the run that *is* an environment resolves that
  environment's own overrides too. The partition key separates projects *and* env
  classes, so a value can never be read across the class boundary
  (`platform/aws/provider/vars/keys.go:112-120`).

Consequence: moving a value from the root to a folder never changes which
environment it belongs to.

### What `ocel dev` does differently, and where it is knowingly limited

- **Precedence is shell < control-plane < dotfile.** The child's environment
  merges, in increasing precedence: the CLI's inherited environment, project env
  vars, live values, the `.env` dotfile, then each resource's resolved entries,
  with the app-folder binding stated last
  (`cli/internal/cli/dev.go:381-384,410-427`). **The dotfile wins**, because a
  feature whose premise is "getting started needs no cloud account" collapses the
  moment editing that file stops deciding (`dev.go:394-397`). A shell-only value
  therefore does **not** satisfy the discovery gate; the refusal says so by name
  (`cli/internal/cli/devenv.go:50-64`, `shellHint`).
- **A root `.env` line broadcasts to every folder in the key's scope** — the same
  value everywhere, with no per-folder divergence in dev
  (`cli/internal/devserver/values.go:54-58`). A scoped variable has no root cell
  at all (`proto/resources/v1/env.proto:44-49`).
- **There is no app identity in dev, and this is a documented limit rather than a
  bug.** Dev spawns one child for the whole project and nothing tells it which
  app that child is, so `EnvUpdate` carries one unkeyed flat map
  (`cli/internal/devserver/devserver.go:303-308`; `dev.go:388-392`). A two-app
  project can therefore only state the project root, and a scoped read still
  throws under it. Where a key some app binds is scoped to a folder this run
  cannot state, `ocel dev` refuses up front and names only the apps binding a
  folder in that key's scope (`cli/internal/cli/devenv.go:110-160`,
  `checkStatableBinding`; `cli/internal/devserver/devserver.go:217-247`,
  `ScopedFolders`) rather than letting the SDK crash at the first read.
- **Live values do not rotate under a dev run.** They are fetched eagerly at
  startup and held for the life of the process; picking up a rotation means
  restarting `ocel dev` (`cli/internal/cli/dev.go:398-405`). Timing is the whole
  difference — nothing about a call site changes.
- **Secret hygiene is a stated guarantee.** Diagnostics carry key names and line
  numbers only, never a value: the dotfile parser records the 1-based numbers of
  unreadable lines rather than the lines themselves, because an unreadable line
  is exactly the shape a pasted token has
  (`cli/internal/dotenv/dotenv.go:35-42`), and the dev refusal is handed the
  file's key names and not its values (`cli/internal/cli/devenv.go:25-30`).

### Rejected

- **A fourth, "client" class.** Client-accessibility is orthogonal to
  confidentiality and only `plain` can honour it, so it is a boolean the
  compiler checks at the declaration rather than a class that would have to
  answer what its encrypted variant means.
- **Calling `secret` "live" on the wire.** Considered and dropped: liveness is a
  consequence of the delivery mechanism, not a separate axis, and a fourth enum
  value would have to be reconciled with the three real delivery paths.
- **Making the staleness bound per-variable.** Rotation latency is an
  operational property of the project, and a per-variable bound multiplies the
  fetches without changing what anyone can reason about (`live.go:21-24`).
- **Named-environment overrides counting toward the gate for every run.** They
  would let one environment's value stand in for the class-wide value every
  other environment reads. Narrowed by the amendment below: an override counts
  for the run that is the environment holding it, and for no other.

## Consequences

- **`plain` and `sensitive` share a rotation cost, and `secret` does not.** Any
  guidance that says "use the encrypted class for secrets" is wrong twice over:
  it overstates the confidentiality and it silently commits the user to a
  redeploy per rotation.
- **A `sensitive` value's blast radius equals `lambda:GetFunction`.** Anyone
  auditing IAM should treat that permission as read access to every `sensitive`
  value in the account, and should not treat `sensitive` as a compensating
  control for a broad Lambda-read grant.
- **`ocel dev` has no membrane, so every class lands in the child in plaintext
  under its own name.** Confidentiality is a deploy-time property; a dev run
  trades it away and says so where the file is read
  (`cli/internal/cli/devenv.go:176-184`, `reportDotfile`).
- **`provision.FetchLiveValues` is a stub** with its final signature — it returns
  an empty map today (`cli/internal/provision/provision.go:76-91`). Dev's
  live-value semantics are therefore described here as designed, not as
  exercised.
- **A client-accessible value is frozen into the browser bundle at build time,
  whatever its class.** It is delivered to the app build under the framework's
  public prefix and inlined by the framework's own static replacement, so the
  bytes a browser receives are a copy taken at build time. No class escapes
  this: `plain` is the only class client access is allowed on, and even a
  rotation of that value leaves already-built pages serving the old one until a
  build replaces them. The rotation guarantee stated above is a server-side
  guarantee; the docs must not let it be read as a client-side one.
- **A `--prebuilt` deploy whose bundle predates a change to a client-accessible
  value is refused.** A vars-only deploy reuses build output and so skips the
  step that hands client values to the framework build. Letting it proceed would
  ship a Deployment whose server side holds the new value and whose browser
  bundle holds the old one — the same key disagreeing with itself across the
  wire, silently. Refusing names the changed key and the remedy: a full build.
  Both halves of the rule live in the CLI, since the build environment and the
  reuse decision are both its.
- **A gate refusal is recoverable in a terminal, and the abandoned case is not.**
  `ocel deploy` and `ocel preview up` now open the bundled vars UI on the same
  gate and provider session, block, and resume into the build; resuming forces a
  fresh gate and a second discovery pass rather than re-reading the store's old
  verdict (`cli/internal/cli/gate_recovery.go:45-70`). Non-interactive runs, and
  runs opting out with `--no-ui` or `OCEL_NO_BROWSER`, keep the hard refusal.
  **A closed browser tab is not detected**: `varsui` has no browser-close signal,
  so `Session.Wait` only reports `ErrAbandoned` when a caller closes the session
  itself, and a developer who closes the page mid-wait leaves the command
  blocked until Ctrl-C. The abandonment presentation is implemented and tested
  but unreachable in production — bd `ocelhq-szmy`, gated on `ocelhq-xd5j.36`.
- **Evidence is still mostly unit-level.** `cli/internal/envwire` now drives the
  real `ocel/env` SDK in a real node process against the real generated Connect
  handler in front of a real `envgate.Gate`, which closes the arrival and
  source-map half of the structural gap for the deploy path. The `devserver`
  twin is open: `ocel dev` spawns node from its own call site with its own flag
  list and its own copies of the `OCEL_PHASE` / `OCEL_DEV_SERVER` names, and
  nothing runs the real SDK against it (bd `ocelhq-xd5j.43`).
- **Multi-line quoted `.env` values (PEM keys) are not supported.** The dotfile
  parser reaches single-line parity with the `dotenv` the framework itself uses;
  a value spanning lines is deferred as bd `ocelhq-3ec6`, and `KEY: value` is
  deliberately not read.

## Open question

**A scoped key that no app binds is completely silent.** `checkStatableBinding`
refuses only for a key some app's own binding is in the scope of, and stays
silent about a key scoped where no app is bound, on the grounds that such a key
is unreadable under every binding — including the ones a deploy states — so dev
should be as silent as a deploy rather than refusing a project that deploys
(`cli/internal/cli/devenv.go:118-123,127-141`).

The counter-argument, deliberately recorded and not acted on: such a key is dead
weight in dev *and* in deploy, and in practice it is usually a typo in a
`folders:` list. A one-line "nothing will read this" notice would catch that at
the moment it is cheapest to fix. The reason it is not here is consistency —
dev and deploy currently agree to say nothing, and a notice in only one of them
is a new divergence. Whoever revisits this should change both surfaces or
neither. No bead has been filed; this paragraph is the record.

## Amended: an override resolves for the environment holding it, at both read times (2026-08-02)

The orthogonality above stands; what has changed is that the class-wide → named
environment axis is now *resolved* rather than merely *recorded*. When this ADR
was written, an override was a row the store held and the matrix displayed, and
nothing read one — the text said the matrix showed them "purely so a cell that
reads empty while an override survives is not a lie." That is no longer the
whole story. Read the two passages it affects — the second bullet of "The two
override axes are orthogonal", and the Rejected entry on overrides and the gate
— as narrowed by this section.

**The rule.** A cell resolves from the override the reading environment holds,
and from the class-wide value where it holds none. Which environment is reading
is decided once per read time:

- **At deploy**, by the run's own scope. `ocel preview up --name staging` states
  `staging` (`envgate.Scope.Environment`, set at the `envScope` call site), and
  the gate reads the class-wide set overlaid with that environment's overrides.
  Every baked value the build inlines comes from that overlay.
- **At runtime**, by the coordinate manifest. A preview deploy pins its own
  identity into `live.Manifest.Environment`; the membrane fetches both addresses
  in the one query it already makes and prefers the override
  (`bootstrap.resolved`). Production pins none and reads a single cell per key.

Both read times therefore agree, which is the point: a value is not resolvable
by one class's delivery mechanism and invisible to another's.

**Two consequences the original text got the wrong way round.**

1. **An override does satisfy the gate — for the run that is that environment.**
   A required key only `staging` holds a value for is not a gap for `staging`;
   it is `staging`'s value. Refusing there would refuse the deploy of a branch
   whose configuration is complete, and would disagree with the live path, which
   serves that same row. For every *other* run, including every production
   deploy and any run bound to no environment, an override still counts for
   nothing — which is the original rejection, intact and narrower.
2. **The matrix stays class-wide, and its app readout does not.** A cell's `Set`
   and `Version` are the class-wide value's, because the UI manages every
   environment's values at once rather than standing in any one of them; the
   overrides sit beside it. `AppResolution.Missing` is the run's own view and is
   read from the resolved overlay, so what the UI calls owed and what the deploy
   refuses on stay one answer.

**What has not changed.** Production has a single environment, so it holds no
overrides at all: `SetValue` refuses one addressed at a production coordinate,
and the deploy pins no environment there. An override may only be written
against an environment identity the provider enumerates, because that identity
is exactly the key the runtime derives — and the provider holds that rule, not
the CLI, which can be skipped. An override outlives the removal of the preview
that read it (`ocel preview rm` removes compute, not values), so a rebuilt
branch finds the value someone deliberately set; an override whose environment
is gone is an **orphaned override** — surfaced as such by `ocel env ls` and the
UI, and removable without the environment coming back.

## Amended: what a reference does to the read grant (2026-08-02)

A cell may now hold the address of another project's cell rather than a value
(`vars.Store.SetReference`; **Reference** in `CONTEXT.md`). Nothing about the
three classes changes — a reference is what a cell holds, not what class it is —
but two sentences above are narrowed.

**The partition-scoped grant covers more than one partition.** A reference is
resolved where it is read, by one further query, so a function reading a
live-class value through one reads the *owner's* rows. `varsReadPolicy` is
therefore conditioned on this project's partition plus the partition of every
project *that app's own live values* resolve out of. The provider reads which of
the project's cells hold an address before the deploy runs
(`server.referenceOwners`), and the deploy narrows that to the cells named in
each app's own coordinate manifest (`deploy.referencedOwners`) — a grant is per
function role, so another app's reference, and a `plain` or `sensitive` cell no
runtime ever reads, widen nothing. It stays a `dynamodb:Query` over an
enumerated list of partitions: no wildcard, no `GetItem`, and no reach into a
project this function does not read from. The isolation argument is intact; its
statement is now "the partitions this function resolves values out of" rather
than "its project's own".

**The consequence is a deploy, not a rotation.** Rotating a referenced value
still needs nothing — that is the whole point, and the staleness bound above
governs when a running function picks it up. But *re-pointing* a reference at a
project this one has never referenced before widens the set of partitions its
functions read, and a running function's grant was written at its last deploy.
Least privilege is what a value's blast radius is bounded by, and the
alternative — granting every project in the class up front — would give away the
isolation this table's third column claims.

**The class key is shared across projects, and has to be.** The key is per class
and never per project (`bootstrap.varsResources`), which is what lets a
referenced value decrypt in the consumer's function at all. A per-project key
would make cross-project references undeliverable rather than make anything
safer: the boundary a `secret` value has is the env class, and it always was.
