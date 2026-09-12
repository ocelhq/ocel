---
version: 1
slug: "app-dashboard-projects-slug-page-tsx"
primary_target: "app/(dashboard)/projects/[slug]/page.tsx"
related_targets: []
---

# Project overview: service map

Scope: `app/(dashboard)/projects/[slug]/page.tsx` and its canvas. Visitor mode: Operate.

Audience and job: a teammate, or the deploying developer days later, asking "what is out
there for this project right now". Orient in one glance, trust the "as at" stamp, drill
into one app or resource. The page mutates nothing.

Proof and content: the latest deployment record the CLI posted for the selected
environment. Apps, resources, usages as edges, variable names and classes, binding
property keys and grants, urls, provider and region, promotion id, tag, outcome.
No values, no git, no durations, no cloud ids.

States in build order: never deployed, last deploy failed (prior success shown beneath a
failure banner), torn down (ghosts), load error, loading skeleton, data at 1 to 20 apps.

Untouched: the CLI, sidebar, sections, sibling pages, the environment query parameter.
Anti-goals: any mutating control, live status, persisted node positions, a second accent.

Open decision recorded: a faint dot ground at `--grid` is allowed on canvases only, as the
one exception to the dashboard register's no-grid rule.

## Direction contract

THESIS: The page is the sheet. The canvas fills the content area under the header; no
title block, no cards-in-a-grid dashboard. It refuses the metric-tiles-plus-table default.

OWN-WORLD: Ocel's docs register. Paper ground with a faint dot grid, hairline-bordered
square tiles, mono uppercase labels in Steel, dashed 1.5px Steel curves at 60% with no
arrowheads, Go dot for success, destructive dot for failure, electric only on focus and
the selected edge. Framework and provider marks from simple-icons and public/frameworks.

STORY: The reader sees which apps are live, on which provider, reading which resources,
as at a stamped moment; clicks one tile to read its variables, bindings, and the files
that caused each edge; leaves knowing the shape of the project without a terminal.

FIRST VIEWPORT: Top-left pinned strip: environment switcher, then the provenance line in
Label type ("as at 2h ago · promotion 7f3a · v1.4.0 · aws · eu-west-1"). Top-right: fit,
zoom in, zoom out. Apps in a left column, resources in a right column, auto laid out and
fitted. Selecting a tile opens a right panel with a hairline edge (bottom sheet under md).

FORM: Infinite canvas, brief-pinned by the owner; no concept roll was run for a pinned
structure. Code-led: no image generation in this harness.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish
review, the verdict, DESIGN.md, and every shipping raster carrying its provenance.
