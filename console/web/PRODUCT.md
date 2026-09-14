# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Developers and their teammates, signed in to an organization. Technical, treated as
such; jargon that is not axiomatic is still avoided.

- **Checking what is live.** A teammate, or the deploying developer days later, opens a
  project asking "what is actually out there right now" without opening a terminal.
  They orient, trust the timestamp, and drill into one app or resource.
- **Approving a device.** Someone mid `ocel login`, in a terminal and a browser side by
  side, pasting a code.
- **Reading dev values.** A developer whose `ocel dev` pulls the project's dev env
  values and resources from the console.

## Product Purpose

The console is Ocel's optional hosted control plane and the visual half of a CLI-first
product. It is a viewer: it shows what the CLI did and where things landed in the
customer's own cloud. Everything starts from the CLI except creating an account and an
organization. The console never triggers a deploy, creates a project, or edits
infrastructure.

Success is a reader understanding a project's state faster than they could from the
terminal, and trusting that what they see is exactly what the CLI last reported.

## Positioning

- **A viewer, not a cloud.** Infrastructure lives in the customer's account. The
  console holds only what the CLI reports to it and what a connector lets it pull.
- **CLI-first.** Every mutating action the console cannot perform names the command
  that can. The command pane is the console's call to action.
- **Own the history, borrow the state.** The console stores events the CLI pushes
  (deployments, and the topology each one landed) so history survives pruning in the
  customer's cloud. It never stores live state that belongs to the customer's account
  (variable values, logs, spend); it pulls those through a connector when one exists.

## Operating Context

- Link flow: `ocel login` points the CLI at a console URL; `ocel link` ties a local
  project to a console project (the link is a local sidecar, not a server record). The
  project appears on the console with nothing to show until the first deploy.
- A deploy runs from the developer's machine into their provider account and, once
  finished, reports a single terminal record to the console: identity, environment,
  provider, outcome, the apps with their live urls and variable names, the resources
  and bindings, and the app-to-resource usages that form the graph.
- Environments are production and preview. The console shows one environment at a
  time, selected in the header; the selection is a query parameter today.
- Data on the console is "as at last deploy", never live.
- Runs locally on port 3000 against a Postgres from `docker-compose.yml`; the CLI reaches
  it through `OCEL_CONSOLE_URL`.

## Capabilities and Constraints

**Ships today** (the repo is the source of truth):

- Sign-in with GitHub, organization creation and switching, device approval.
- Organization settings: rename and re-slug, leave, delete. Members: invite by link (no
  email is sent), change roles, remove, accept an invitation at `/invite/<id>`.
- Project registry: name, slug, description, detected frameworks.
- Dev env values and per-user dev resource resolution for `ocel dev`.
- Blob presigning for uploads.
- Navigation for deployments, variables, resources, domains, monitoring, and spend.
  Every one of these pages is a notice naming the CLI command that does the job.

**Being built:** the deployment record the CLI pushes after a deploy, and the project
overview that renders the latest one as a service map.

**Planned, not shipped.** Show only when clearly labelled as planned:

- Connectors: an API server in the customer's account the console calls to pull
  variable values, logs, and spend. Same model as CLI-to-provider.
- Monitoring and spend backed by real data.

**Undecided:** whether the console ever gains a mutating action beyond account and
organization setup.

**Status:** pre-release alpha. Nothing is released, so nothing has consumers, and the
codebase makes clean breaks.

**Terminology:** "your own cloud", "provider", "environment" (production, preview),
"promotion" (one deploy of the whole project), "deployment" (one app inside a
promotion), "app", "resource", "binding", "usage" (an app reads a resource), "slug",
"link", "connector", "console". Never call the console or Ocel a cloud.

## Brand Commitments

Binding, confirmed by the owner:

- **Same identity as www.** `ocel`, lowercase wordmark, the cut-ring mark, the electric
  blue accent, and the shared token set in `app/globals.css`. The console is the docs
  register of the www design system: paper, hairlines, and type, none of the landing
  page's drafting apparatus.
- **Icons and brand marks.** Phosphor for interface icons, simple-icons and the inline
  SVG components under `components/marks/` for framework and vendor marks. A thing with
  a brand mark is shown with it.
- **Voice.** Plain, direct, no hype. Every empty state names the command that fills it
  and says why the console cannot. Never use words to describe what a command or
  record shows better.
- **Open source.** MIT, public at github.com/ocelhq/ocel.

## Evidence on Hand

- The project registry and framework catalog in `lib/frameworks.ts`.
- The CLI's local records after a deploy: `.ocel/deploy-result.json` and
  `.ocel/service-map.json`, the provider contract manifest under `proto/`. These define
  what a deployment record can truthfully carry.
- **Absent, do not fabricate:** git sha or author of a deploy, build durations, cloud
  resource ids, logs, metrics, spend, queue or channel resources, live status of
  anything. Demonstration data for the overview must be labelled synthetic.

## Product Principles

1. **Show what the CLI reported, stamped with when.** Every view of infrastructure
   carries its "as at" moment and the promotion it came from.
2. **The command is the action.** Where the console cannot act, it names the command
   and why, in a terminal pane, not a disabled button.
3. **Events are owned, state is borrowed.** Store what the CLI pushes; pull what lives
   in the customer's account; never cache the latter.
4. **Empty and error states are the first screens most readers see.** They are designed
   before the data state, with the same care.
5. **One identity with www.** A new token on the console is a new token on the site.

## Accessibility & Inclusion

No product-specific standard was established. Commands, slugs, and records are primary
content and must remain real text, never images.
