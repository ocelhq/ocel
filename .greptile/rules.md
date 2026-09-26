# Review rules

Every change is reviewed against these rules. Flat set, no precedence — except
**Blast radius**, which blocks a merge on its own.

Each rule states the target behaviour, then the signals that fail it. Cite the rule name
in a finding.

## Disposition

These rules are decisions already made, not review opinions. A finding that cites a
rule has exactly two dispositions:

- **Fix** — change the code until the rule passes, then re-review.
- **Escalate** — stop and hand the finding to the human, quoting the rule and why
  the code cannot satisfy it.

Waiving, downgrading, or arguing a rule finding inside a diff is outside the
implementer's authority. Disagreement with a rule is a PR against this file, decided
by a human — never a judgment call made mid-change. A change is complete only when
review returns zero open rule findings or the human has ruled on every escalation.

---

## Blast radius

For every new port, socket, RPC, endpoint, or credential path, determine from the code —
not from a claim in the PR — its access scope and what a compromised credential reaches.

Fails when:

- A credential is static, hardcoded, or long-lived. Provider-native identity, an assumed
  role, or a short-lived token instead.
- An IAM policy wildcards `Action` or `Resource`, or one role serves more than one project
  or environment. Scope to the naming scope the deploy already owns.
- A listener binds beyond loopback without authn and authz in the code path.
- An RPC is added with no enforcement of who may call it.
- A secret can reach CLI output, an error string, a log line, or telemetry.
- Customer state crosses an account boundary that the change does not justify.

## Growth term

Ocel's own request cost per deploy stays negligible against the resources it provisions.
Before classifying a call, ask whether it needs to happen at all.

Classify every provider call the diff adds by how its request count grows:

| Growth term                                     | Verdict                        |
| ----------------------------------------------- | ------------------------------ |
| `O(1)` per deploy                               | pass                           |
| `O(resources in this deploy)`                   | pass                           |
| `O(projects / stacks / objects in the account)` | fail                           |
| `O(deploys ever made)`                          | fail — each run taxes the next |

Also fails when a loop polls without backoff or a cap, immutable data is re-read instead
of cached, or per-item calls exist where the API offers a batch.

Budgets, where one deploy is one stack created and destroyed:

| Metric                                              | Target | Fail |
| --------------------------------------------------- | ------ | ---- |
| Ocel's provider request charges per 10,000 deploys  | $2     | $10  |
| Billable provider requests per deploy               | 50     | 250  |
| Share of the customer's bill for the same resources | 0.1%   | 1%   |

Bytes a deploy leaves behind after teardown must be zero — its own teardown reclaims what
it wrote. Retention is the only term that keeps growing after the deploy is over, so a
destroy that leaves state behind fails this rule even when its request count passes.

Prior failure: Pulumi's S3 state backend enumerated all stacks per operation, so LIST
requests scaled with live objects in the bucket. A measured run of 548 stacks cost $0.236
— $4.31 per 10,000 deploys — of which 78% was enumeration rather than state writes, and
each run left husks that made the next enumeration dearer. The backend writes 9 PUTs per
operation, putting the structural floor near $1 per 10,000 deploys. An `O(deploys ever)`
term that passed review.

## Enumeration

Reach a known key directly. Bound every listing by a prefix naming one project or one
deploy.

Fails when:

- A `List*` / `Describe*` / `Scan` runs with no prefix, filter, or bound tied to the
  current deploy.
- Code pages "until done" over a bucket, table, or namespace shared across projects.
- Something is enumerated to find one item whose key is derivable from the naming scope.
- A listing's page count grows with the account's age rather than the deploy's size.

## Naked call

A provider SDK call made outside the Pulumi engine carries retry with exponential backoff
and jitter, and treats throttling as an expected response.

Pulumi's providers already do this. A naked call inherits none of it.

Fails when:

- A raw client call has no retry policy.
- Backoff has no jitter, or no ceiling.
- A throttle or quota response (`ThrottlingException`, HTTP 429, `Retry-After`) is handled
  as fatal.
- Retry wraps errors that will never succeed on retry.

## Fan-out

For every changed path, ask: if a user ran this 50 times concurrently, what fails? The
code must already handle the answer.

Fails when:

- A per-deploy path writes to a key shared across deploys — one parameter, one record, one
  file — without a compare-and-set.
- Per-deploy work could have been done once at bootstrap.
- Parallelism is unbounded against a provider quota, with no backpressure.

Ask "why is this per-deploy at all?" before "how do we make it concurrent-safe?" A
`PutSSMParam` on every preview deploy failed this rule: a per-deploy write to a shared
parameter that neither needed to happen nor scaled.

## Dumb caller

The CLI speaks only proto. Provider mechanics stay behind the contract.

Fails when:

- `cli/` names a provider, ARN, region, or vendor service.
- The CLI branches on which provider is configured.
- The CLI parses a provider error string.
- A behaviour change ships by editing the CLI rather than the proto and the provider.
- A proto message carries one provider's concepts.

## Contract

For every changed abstraction, ask: could a second origin cloud or edge land as a sibling
directory, with no branch added to existing code? If the diff makes that harder, it fails.

Ocel separates origin from edge — AWS origin with Cloudflare edge is a supported pairing,
and `platform/edge/contract` is what both sides agree on.

Fails when:

- A provider-specific type appears in a shared package.
- An interface is shaped to one SDK's surface.
- Edge behaviour is defined by one vendor's capabilities rather than by the edge contract.
- An import crosses from one vendor into another.
- Code assumes origin and edge come from the same vendor.

## Naming

The Go kits (`pkg/providerkit`, its siblings under `pkg/`, and the vendors under
`platform/` that implement them) name things with words people already use, and a
provider declares what it can do where the compiler checks it.

1. **A name is a word people already use for the thing**, in this domain or in English:
   `Provider`, `Ledger`, `Cipher`, `Certificates`, `Connector`, `Stacks`. A role minted
   from a verb never is. The `-er`/`-or` test catches minted roles only: a word already
   established for the thing keeps its name whatever its suffix (`Provider`, `Connector`,
   `Ledger`, `Cipher`, `Container`).
2. **A required port is a noun accessor on `Provider`; an optional step is a verb-named
   func field on `Hooks`.** Nil means absent. Steps that only make sense together are one
   nested group, a pointer that is nil as a whole, so the compiler keeps them paired.
3. **A yes/no or constant about a provider is a `Facts` field.**
4. **One word, one meaning across the kits.**
5. **A method never repeats its receiver's noun.**
6. **One vendor file per port, per hook, or per hook group, named after it.** The
   vendor's root file holds the constructor, the `Facts` literal, the `Hooks` literal and
   the port accessors, and nothing else.

Fails when:

- A type is an `-er`/`-or` role minted from a verb (`Prober`, `Certifier`, `Releaser`,
  `Sealer`, `Reporter`, `Tracer`, `Warmer`, `Pricer`, `Fetcher`) rather than the word for
  the thing it hands over.
- Two hooks that are only ever set together are separate fields rather than one group.
- Code type-asserts a provider, a port, a program or an `impl any` to discover an optional
  capability. A typo in a method name then drops the capability without a compile error.
- A provider answers a yes/no or a constant through a method or a marker interface
  instead of a `Facts` field.
- A word already given a meaning in the kits is used for a second thing.
- A method repeats its receiver's noun (`Certificates.IssueCertificate`,
  `Runtime.ContainerRuntime`).
- A vendor's port, hook or hook group body lives in its root file or in a file named for
  something else, or a `var _ provider.X = (*Provider)(nil)` assertion stands in for
  the `Facts` or `Hooks` literal that already type-checks it.

## Signal
 
The code carries the explanation; comments carry only what the code cannot. Zero comments
is the baseline. A comment is justified only as a marker of an intentional gap — `TODO`,
`FIXME`, `HACK`, a known breakage — never as a description of the code beneath it.
 
Exception: published packages (`packages/`, the Go `sdk/`, the Python `python/`, the
Rust `crates/`) carry public, user-facing doc-comments on their exported surface. Those
document the API for a consumer, not the implementation for a maintainer. A fixture
config is the other exception: it shows a variant that conflicts with what is live as a
commented-out line, with one line saying when to pick it, and nothing more.
 
Fails when:
 
- A comment restates what the adjacent code does.
- A comment narrates a step ("loop over stacks", "build the request") or a section
  ("--- helpers ---").
- A comment explains code that should instead be renamed, split, or simplified until it
  needs no explanation.
- A gap marker carries no actionable content — a bare `TODO` with no what or why.
- An exported symbol in `packages/`, `sdk/`, `python/` or `crates/` lacks a doc-comment,
  or its doc-comment describes internals rather than the contract a user consumes.

## Clean break

<!-- TODO(alpha): remove this rule when the first non-alpha version is released. -->

Nothing is released, so nothing has consumers to protect. A change replaces the old
behaviour outright: one code path, the new one, and the old one is deleted in the same
diff. "It would break existing users" is not a valid finding — there are none — and
review must not request compatibility with prior unreleased behaviour.

Fails when:

- A deprecated symbol, flag, field, or endpoint is kept alongside its replacement instead
  of being deleted.
- A shim, alias, adapter, or fallback exists only so an old shape keeps working.
- Code branches on a version, format, or schema that no released artifact ever produced.
- A migration path is written for state only unreleased builds could have created.
- A rename is done halfway — the old name re-exported, wrapped, or left accepting input —
  rather than renamed everywhere in one diff.
