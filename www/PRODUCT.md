# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Developers and the agents they work with. Technical, treated as such; jargon that is
not axiomatic is still avoided.

`www` serves three situations at once, because it hosts both the landing page and
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

Ocel deploys apps. Ocel is the deployment engine: point the CLI at a project and it
builds, provisions, and ships to a target with as little configuration as possible.
A provider target is the customer's own account, under their billing and access.
Ocel Cloud is the zero-config managed target, for a project that names no provider.
The console is the UI over Ocel, never called a cloud; it may offer Ocel Cloud.

`www` is ocel.dev, the open-source engine's home. It exists so a developer can
understand what Ocel is and why they should care, get an app running in their own
account, and find the reference they need without having to search for it. It is also
the one landing page: ocelhq.com, Ocel Cloud's own site (`cloud/www/PRODUCT.md` in the
private cloud repo), is parked and redirects here until after launch. Ocel Cloud appears
on ocel.dev as one target among the others, never as a section of its own.

## Positioning

- **Own account, not a platform.** With a provider target, infrastructure is created in
  the customer's AWS or GCP account or on their Linux box, and nothing of the app runs on
  Ocel's side. Ocel Cloud is one more target, never a dependency: the engine is whole
  without it, its implementation is private, its contracts (`proto/cloud/v1`) are public.
- **The app never changes when the target does.** The same code deploys to a VPS, AWS,
  GCP, or Ocel Cloud; moving between them is the config's provider line, not a rewrite.
- **Zero infrastructure code.** One config names a target and nothing about how to build
  there. Swapping cloud for VPS, or serverless for container, is one line; the app does
  not know or care.
- **Application-defined infrastructure.** A database or bucket is a function call in app
  code, and the call is the provisioning step. There is no second definition to keep in
  sync. Proto-backed and language-neutral.
- **Declared resources in dev.** `ocel dev` starts the postgres and buckets the app
  declares in containers on the developer's machine. Docker is the only prerequisite and
  no account is needed. Values a team shares come from the dev tier's `envSource`, such
  as Infisical, with `.env.local` on top.
- **Not an IaC replacement.** Ocel does not try to replace Terraform, Pulumi, or SST and
  can interoperate with them (`tests/fixtures/iac/with-pulumi`,
  `tests/fixtures/iac/with-sst`). Never frame it as "a plain alternative" to them.

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

- CLI commands: whatever `content/docs/cli/` documents (deploy, dev, preview,
  promotions, rollback, env, domain, logs, skill, and more). Read the folder; never
  hand-copy the list into a page.
- SDKs: TypeScript (`packages/ocel`), Go (`sdk/`), Python (`python/ocel`), Rust
  (`crates/ocel-sdk`).
- SDK resources: postgres, bucket, kv, realtime, task, topic, and workers that consume
  them (`proto/` resource types).
- Providers: AWS (`platform/aws`), GCP (`platform/gcp`), VPS over SSH (`platform/vps`),
  Cloudflare as an independent edge in front of any origin (`platform/edge/cloudflare`).
- Frameworks: Next.js (`frameworks/next`) and SvelteKit (`frameworks/sveltekit`) have
  dedicated adapters; plain Node servers (Express, Fastify, Hono) are covered by
  `examples/`. Python and Rust apps deploy as containers.
- IaC interop: `packages/ocel-pulumi`, `packages/ocel-sst`.
- Agent skills: `skills/`, installed with `ocel skill install` or `npx skills add ocelhq/ocel`.
- Ocel Cloud as a target: `ocel deploy` and `ocel preview up` with no provider, when
  logged in to a console that offers it (CLI side in ocel#1998, cloud side in cloud#1).
- Console (github.com/ocelhq/console, Apache-2.0, self-hostable): sign-in, device
  approval, organizations and members, project linking, deployment history, variables.

Covered without a dedicated adapter, and fine to show as supported: Django and any other
WSGI or ASGI app (a Python app whose entrypoint is `wsgi.py`, `asgi.py` or similar,
`cli/internal/language/detector.go`), and DigitalOcean, Hetzner or any other Linux box
through the VPS provider.

**Planned, not shipped.** The site may show these only when clearly labelled as planned
or coming soon. Never present them as available. Check the code before adding to this
list, or before trusting it.

**Status:** pre-release alpha. Nothing is released, so nothing has consumers. The
codebase makes clean breaks; the site must not imply stability guarantees it does not
have.

**Terminology:** "your own cloud", "provider", "target", "slug", "app", "resource",
"console", "link", "binding", "Ocel Cloud" (the zero-config managed target). Say
"deploys apps to your own infra", never "brings the DX of X to Y". Never call the
console or Ocel a cloud, and never call the console Ocel Cloud.

## Brand Commitments

Binding, confirmed by the owner:

- **Name and mark.** `ocel`, lowercase wordmark, with the cut-ring mark as implemented in
  `components/logo.tsx`. The identity is final.
- **Electric blue accent.** The `--electric` color is a brand color, not a docs-theme
  choice.
- **Open source.** Apache-2.0, public at github.com/ocelhq/ocel, and the site says so.
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

- Real, runnable code samples: the quick start, `ocel.json` variants, and the
  TypeScript, Go, Python and Rust SDK samples in `content/docs/index.mdx`.
- Framework and provider logos in `components/logos.tsx` (check each against
  Capabilities before showing it as shipped).
- Working example apps under `examples/` at the repo root and `../examples/` in the
  ocelhq workspace.
- Some docs pages under `content/docs/` are still placeholders ("Dummy content.", e.g.
  `frameworks/nestjs.mdx`).
- Recordings of the real CLI, console and an agent at work, listed with their shot
  descriptions in `components/landing/clips.ts`. They are the landing page's figures; a
  clip not yet recorded draws its shot description in its place.
- **Absent, do not fabricate:** customers, testimonials, case studies, press,
  benchmarks, pricing, uptime or cost claims, and any user count.
- **Absent, do not fabricate:** customers, testimonials, case studies, press,
  benchmarks, pricing, uptime or cost claims, and any user count.

## Product Principles

1. **Say only what ships, and label the rest.** Roadmap may appear; it is always marked
   as planned.
2. **Show, then explain.** Code and terminal output make the claim; prose sets it up
   and gets out of the way. The reveal of how it works comes after the magic, not
   alongside it.
3. **Every ask states its why.** A prerequisite without a reason is a prerequisite to
   cut.
4. **Own cloud is never second-class.** ocel.dev is the engine that deploys into the
   reader's own account and sits beside their existing IaC rather than replacing it.
   Starting on Ocel Cloud is a shortcut into that story, not a different one.
5. **Ocel Cloud is the quickest target, not a pitch.** The quick start leads with it
   because it is genuinely the fastest path (no account, no keys), and the own-infra
   path sits one step away, complete and working without anyone's permission. Beyond
   that, Ocel Cloud appears only where a reader of the engine meets it anyway (the list
   of targets, `ocel deploy` with no provider, its error codes), described as plainly
   as AWS. No banners, upsells, or pricing on the open-source side. On the landing it
   is a name in the hero's list of targets, one of the three windows in "Change the
   target", a project in the console still, and the waitlist beside it at the foot of the page.
   Ocel Cloud is by invite during the alpha; the waitlist is how people ask.
6. **Agents are first-class, and the site shows it.** The skill, `--json` everywhere,
   stable error codes with docs URLs, a self-describing binary, `llms.txt` and `.md`
   pages are product, not footnotes (`content/docs/agents.mdx`).
7. **Docs are the product for most visitors.** The landing page opens the door; the
   docs are where trust is earned, so their reading experience is a first-class concern.

## Accessibility & Inclusion

No product-specific standard was established. In docs, code samples and terminal output
are primary content and must remain real text, never images. The landing is the one
exception, by the owner's decision: its figures are recordings of the real thing. What a
reader copies or reads closely stays text there too (the install command, the config
diff, resource and command names). Every clip carries a label describing what happens in
it and a Pause control, and clips hold on their poster under reduced motion.
