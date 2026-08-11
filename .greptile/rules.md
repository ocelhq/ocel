# Review rules

Every change is reviewed against these seven rules. Flat set, no precedence — except
**Blast radius**, which blocks a merge on its own.

Each rule states the target behaviour, then the signals that fail it. Cite the rule name
in a finding.

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

Prior failure: Pulumi's S3 state backend enumerated all stacks per operation — LIST
requests scaled with live objects in the bucket and cost 3.5× the state writes themselves.
An `O(deploys ever)` term that passed review.

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
