# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Developers and the agents they work with. Technical, treated as such; jargon that is
not axiomatic is still avoided.

`www` serves three situations at once, because it holds both the landing page and
the docs:

- **Comparing.** A developer with a Next.js or Node app who wants hosted-platform DX
  but must, or wants to, run in their own AWS account or on a VPS. Arrives weighing
  options.
- **Shipping.** Someone already decided on Ocel who lands to install, deploy, and look
  up CLI and SDK reference. For them the docs are the product and the landing page is
  a doorway.
- **Owning infra.** A platform or infrastructure engineer for a team, evaluating how
  Ocel sits next to the Terraform, Pulumi, or SST they already run.

## Product Purpose

Ocel deploys apps to your own cloud. Point the CLI at a project and it builds,
provisions, and ships into the customer's own provider account with as little
configuration as possible. Infrastructure lands under the customer's billing and
access. Ocel is not a cloud and not managed anything. The hosted console is an
optional control plane, never called a cloud.

`www` exists so a developer can understand what Ocel is, get an app running in their
own account, and find the reference they need without having to search for it.

## Positioning

- **Own account, not a platform.** Infrastructure is created in the customer's AWS
  account or on their Linux box. Nothing runs on Ocel's side except the optional console.
- **Zero infrastructure code.** One config names a target and nothing about how to build
  there. Swapping cloud for VPS, or serverless for container, is one line; the app does
  not know or care.
- **Application-defined infrastructure.** A database or bucket is a function call in app
  code, and the call is the provisioning step. There is no second definition to keep in
  sync. Proto-backed and language-neutral.
- **Real infrastructure in dev.** Link a project on the console and `ocel dev` resolves
  real resources on every machine for the whole team. No emulators, no containers.
- **Not an IaC replacement.** Ocel does not try to replace Terraform, Pulumi, or SST and
  can interoperate with them (`examples/with-pulumi`, `examples/with-sst`). Never frame
  it as "a plain alternative" to them.

## Operating Context

- Three pieces, each building on the one before: the CLI stands alone; the SDK and the
  console do not.
- A user's first run: install from npm, `ocel init` writes one config file offline and
  signs into nothing, `ocel deploy` shows a plan for shared infrastructure and waits for
  a yes. First deploy takes minutes; later ones reuse what exists.
- Evaluation happens in a terminal and a browser side by side. The console adds sign-in,
  device approval, and project linking on top of the CLI.
- Interop with existing IaC is a supported path, not a workaround.

## Capabilities and Constraints

**Ships today** (the repo is the source of truth):

- CLI commands documented under `content/docs/cli/`: init, dev, build, deploy,
  deployments, rollback, destroy, doctor, domain, env, generate, link, login, logout, run.
- SDKs: TypeScript (`packages/ocel`) and Go (`sdk/`).
- SDK resources: postgres and bucket.
- Providers: AWS (`platform/aws`), VPS over SSH (`platform/vps`), Cloudflare as an
  independent edge in front of any origin (`platform/edge/cloudflare`).
- Frameworks: Next.js has a dedicated adapter and serving runtime (`frameworks/next`).
  Plain Node servers (Express, Fastify, Hono) are covered by `examples/`.
- Console: sign-in, device approval, and project linking.

**Planned, not shipped.** The site may show these only when clearly labelled as planned
or coming soon. Never present them as available:

- Python and Rust SDKs.
- Realtime channel and queue resources.
- Google Cloud and DigitalOcean providers.
- Django.

**Status:** pre-release alpha. Nothing is released, so nothing has consumers. The
codebase makes clean breaks; the site must not imply stability guarantees it does not
have.

**Terminology:** "your own cloud", "provider", "target", "slug", "app", "resource",
"console", "link". Say "deploys apps to your own infra", never "brings the DX of X to Y".
Never call the console or Ocel a cloud.

## Brand Commitments

Binding, confirmed by the owner:

- **Name and mark.** `ocel`, lowercase wordmark, with the cut-ring mark as implemented in
  `components/logo.tsx`. The identity is settled.
- **Electric blue accent.** The `--electric` color is a brand color, not a docs-theme
  choice.
- **Open source.** MIT, public at github.com/ocelhq/ocel, and the site says so.
- **Voice.** Plain, direct, no hype. Rules the owner set, binding across landing and docs:
  - Anytime the site asks the user for anything ("you will need X"), it also explains
    why. If it cannot, it probably should not be asking.
  - Like a good magic trick, separate the trick from the reveal. Show the magic by
    default; save the behind-the-scenes for later, possibly a different section, for
    the curious.
  - Never use words to describe what code can, and does, show better.
  - No jargon and no marketing language ("supercharges"). Be specific.
  - Where a thing can be phrased several ways, ask which falls out as a consequence of
    another, and describe the main thing.
  - Pages, and sections within a page, read like a story: answer the question the
    reader has in their head before they think to search for it.

## Evidence on Hand

- Real, runnable code samples: the quick start, `ocel.config.ts` variants, and the
  Node/Go SDK samples in `content/docs/index.mdx`. The Python and Rust tabs there are
  illustrative and not backed by shipped SDKs.
- Framework and provider logos in `components/logos.tsx`: Next.js, Node, Express, Go,
  AWS are shipped; Django, Google Cloud, DigitalOcean are planned.
- Working example apps under `examples/` at the repo root.
- Most docs pages under `content/docs/` are placeholders ("Dummy content.") awaiting
  real writing. The landing page at `app/(landing)/page.tsx` is still the
  create-next-app template.
- **Absent, do not fabricate:** customers, testimonials, case studies, press,
  benchmarks, pricing, uptime or cost claims, and any user count.

## Product Principles

1. **Say only what ships, and label the rest.** Roadmap may appear; it is always marked
   as planned.
2. **Show, then explain.** Code and terminal output carry the claim; prose sets it up
   and gets out of the way. The reveal of how it works comes after the magic, not
   alongside it.
3. **Every ask carries its why.** A prerequisite without a reason is a prerequisite to
   cut.
4. **Own cloud is the whole point.** Every surface reinforces that infrastructure lands
   in the reader's account, and that Ocel sits beside their existing IaC rather than
   replacing it.
5. **Docs are the product for most visitors.** The landing page opens the door; the
   docs are where trust is earned, so their reading experience is a first-class concern.

## Accessibility & Inclusion

No product-specific standard was established. Code samples and terminal output are
primary content and must remain real text, never images.
