# 3. Project identity is the slug; Ocel Cloud sits above the CLI

Date: 2026-07-27

## Status

Accepted

## Context

Ocel layers as **CLI → SDK → Ocel Cloud**, with Cloud at the top and poppable:
the CLI must be usable without it. Earlier implementations never drew that line.
The main artifact was `projectId` — a control-plane UUID minted by `ocel init`,
written into the tracked `ocel.config.ts`, and then used as the key for real
infrastructure: Pulumi stack names, SSM root-stack state, the deployments-store
Durable Object. A cloud identifier had become the identity of the user's own
AWS infrastructure.

It leaked further. `bootstrap`, `deploy`, `preview`, `destroy`, `rollback`, and
`deployments` each opened with a hard Ocel Cloud login gate whose credentials
were then discarded and never sent anywhere — `deploy.go` makes no control-plane
HTTP calls at all. And an identifier pinned in a tracked file cannot be
re-pointed: a clone
could not be linked to a different Ocel Cloud account or project.

## Decision

**`slug` is the sole project identity.** Already a validated DNS label in
`cli/internal/projectconfig`, it is what everything project-scoped derives from:
Pulumi logical/stack names, SSM root-stack state keys, the deployments-store DO
instance. It is treated as immutable — changing it forks a new project with fresh
infrastructure.

**`projectId` is phased out of the config entirely**, from `OcelConfig` in
`packages/sdk/src/config.ts` and from the Go resolver. A leftover `projectId` in
a user config is ignored silently: the project is at `0.0.1-alpha.0` and
unpublished, so a deprecation path isn't worth carrying.

**Hard cut on the wire, no state migration.** `project_id` becomes `slug` across
`proto/deployments/v1/deployments.proto` (10 sites: `Manifest` plus the
destroy/rollback/list/preview messages) and across `cloud/aws/` (`stackName`,
`InfraStackName`, `PreviewInfraStackName`, `AppDeployStackName`,
`PreviewAppDeployStackName`, `bootstrap.Read/WriteRootStackStateFor`). Stacks
keyed on the old UUID are orphaned. `workers/` was already slug-keyed
(`idFromName(slug)`) and is unaffected.

**Cloud state lives outside git**, in `.ocel/link.json` (already gitignored): one
record holding `apiUrl`, `organizationId`, `projectId`, and a cached
`projectName`. This is what makes a clone linkable to a different account or
project. If the effective `--api-url` doesn't match the recorded `apiUrl`, the
directory reads as unlinked.

**The deploy path is cloud-free.** The login gate is removed from all six
commands that carried it — `bootstrap`, `deploy`, `preview`, `destroy`,
`rollback`, and `deployments`; they authenticate to the user's own AWS via the
provider binary and the standard AWS credential chain. `bootstrap` is included
because it is the first deploy-path command a new user runs, and leaving it
gated would falsify the rule below; like the rest, it discards the credentials
it loads and delegates to the provider's Bootstrap RPC, which creates
account-global CloudFormation stacks in the user's own AWS account. The "cloud"
in bootstrap is the user's cloud provider, not Ocel Cloud.

**Ocel Cloud is required for `ocel dev` only.** Dev sandboxes are a cloud feature
by definition; every other command runs on the user's own credentials. A
cloud-free dev mode is out of scope.

**`ocel.config.ts` is optional in dev, required for deploy/preview.** Dev needs
only `discovery.paths` (default `["ocel"]`) and the project root;
apps/domains/provider are deploy-only. The project root is the nearest ancestor
containing `ocel.config.ts` **or** `.ocel/`, falling back to cwd — so a first run
in a fresh clone anchors at cwd and creates `.ocel/` there, and later runs from
subdirectories find the same root.

**`ocel init` makes no Ocel Cloud call** — no auth, no API call. It writes
`ocel.config.ts` with `slug` (defaulting to the slugified directory name) and
`provider: awsProvider()`, and installs `@ocel/provider-aws` with the package
manager detected from the lockfile. It is the prerequisite of deploy/preview.
Cloud project creation moves into the dev link flow: `ocel dev` links inline on
first run (pick or create a project), with `ocel link` / `ocel unlink` for
re-pointing and for non-TTY environments.

**Dev leader election keys on the project root path**, not the project.
`election.Elect`/`lockfile` hashed `projectId`; they now hash the absolute
project root — one dev server per working tree. Two clones of one repo may sit at
different commits with different resource declarations, and the old key had the
second silently inherit the first's resolved env. Election no longer depends on
the link, which simplifies dev startup ordering.

**Slug-drift guard.** Since slug is the only thread to existing infrastructure, a
typo or rename silently orphans production. Before the first stack op, `deploy`
lists the Pulumi backend's stacks (`ws.ListStacks`, already used by
`destroyproject.go`, `destroy.go`, `previewteardown.go`). If no stacks exist for
this slug but stacks exist for others, the confirmation says so explicitly rather
than showing the routine-update prompt. `--yes` bypasses it for CI.

### Rejected

- **Keeping the `project_id` wire name and feeding it the slug.** The name would
  lie, preserving exactly the confusion being removed.
- **A state migration for existing stacks.** Real engineering for an alpha with a
  near-empty user base.
- **A hard error requiring `--new-project` on slug drift.** It would fail every
  genuinely-new project's first deploy.
- **Opportunistic deployment reporting when a link file happens to exist.** It
  would work from a laptop and not from CI, which reads as a bug.

## Consequences

- **The control plane has no production-deployment visibility.** With no
  `projectId` in the config and no auth on deploy, nothing associates a prod
  deployment with a cloud project. This is accepted. If the console needs a
  deployment view, that is a separate design requiring an explicit, opt-in
  mechanism — not a quiet reintroduction of an identifier into the config.
- Deployed stacks keyed on the old UUID are orphaned and must be torn down by
  hand.
- The slug carries real weight: renaming it is a project fork, guarded only by a
  confirmation prompt.
- A fresh clone can deploy with nothing but `ocel.config.ts` and AWS credentials;
  `.ocel/link.json` is needed only to run `ocel dev`.

## Superseded paths (2026-07-30)

The decision above stands unchanged; only where the code lives has moved. The
`@ocel/sdk` package was folded back into the root `ocel` package, so
`packages/sdk/src/config.ts` is now `packages/ocel/src/config.ts` and the
`OcelConfig` it declares is imported as `ocel/config`, not `@ocel/sdk/config`.
