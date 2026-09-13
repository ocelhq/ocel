---
name: Ocel Console
description: The dashboard register of Ocel's design system. A viewer for what the CLI last reported.
colors:
  electric: "oklch(0.4924 0.2858 266.52)"
  electric-dark: "oklch(0.587 0.225 270.16)"
  ink: "oklch(0.1448 0 0)"
  ink-dark: "oklch(0.9575 0.0067 97.35)"
  paper: "oklch(1 0 0)"
  paper-dark: "oklch(0.1809 0.0052 248.12)"
  fog: "oklch(0.9581 0 0)"
  fog-dark: "oklch(0.2077 0.005 248.07)"
  body: "oklch(0.4495 0 0)"
  body-dark: "oklch(0.7034 0.0135 255.53)"
  hairline: "oklch(0.9128 0 0)"
  hairline-dark: "oklch(0.2958 0.0084 255.57)"
  steel: "#8a8a8a"
  steel-dark: "#5f646b"
  faint: "#c9c9c9"
  faint-dark: "#3a3e44"
  grid: "#ececec"
  grid-dark: "#1c1f22"
  go: "#1a9e57"
  go-dark: "#3ecf7a"
  warn: "oklch(0.5423 0.1315 66.11)"
  warn-dark: "oklch(0.8 0.132 76.5)"
  destructive: "oklch(0.577 0.245 27.33)"
  destructive-dark: "oklch(0.627 0.22 25)"
  terminal: "#16181a"
  terminal-rule: "#2a2d31"
  terminal-foreground: "#9aa0a8"
  terminal-ink: "#f2f1ec"
typography:
  title:
    fontFamily: "Space Grotesk, system-ui, sans-serif"
    fontSize: "1.5rem"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "-0.025em"
  headline:
    fontFamily: "Space Grotesk, system-ui, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: 1.4
    letterSpacing: "-0.025em"
  subject:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "normal"
  body:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.25rem
    letterSpacing: "normal"
  detail:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "normal"
  label:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.6875rem"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.14em"
  path:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "normal"
  command:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.8125rem"
    fontWeight: 500
    lineHeight: 1.25
    letterSpacing: "normal"
  wordmark:
    fontFamily: "Archivo, system-ui, sans-serif"
    fontWeight: 800
    letterSpacing: "-0.04em"
rounded:
  none: "0px"
spacing:
  xs: "4px"
  sm: "6px"
  md: "12px"
  lg: "20px"
  xl: "32px"
  dot-gap: "24px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    padding: "0 10px"
    height: "32px"
  button-outline:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "0 10px"
    height: "32px"
  button-outline-hover:
    backgroundColor: "{colors.fog}"
    textColor: "{colors.ink}"
  tile-app:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "12px"
    width: "264px"
    height: "112px"
  tile-resource:
    backgroundColor: "{colors.fog}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "12px"
    width: "232px"
    height: "92px"
  tile-ghost:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.body}"
    rounded: "{rounded.none}"
    padding: "12px"
    width: "200px"
    height: "72px"
  chip:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.body}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "2px 6px"
  map-control:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.body}"
    rounded: "{rounded.none}"
    size: "32px"
  provenance-strip:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.body}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
  details-panel:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "16px 20px"
    width: "360px"
  command-pane:
    backgroundColor: "{colors.terminal}"
    textColor: "{colors.terminal-ink}"
    typography: "{typography.command}"
    rounded: "{rounded.none}"
    padding: "8px 8px 8px 16px"
  table-row:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "0 20px"
    height: "56px"
  badge-live:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    height: "20px"
  popover:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "16px"
    width: "384px"
  input:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "4px 10px"
    height: "32px"
---

# Design System: Ocel Console

Invariants, tokens, register dials and named exceptions live in `ui/theme/DESIGN.md`.
This file covers the operate register's components, and defers to that file wherever the
two disagree.

## Overview

**Creative North Star: "The Engineering Drawing, dashboard register"**

The console is the same drawing as ocel.dev with the drafting apparatus lifted off. Paper
ground, hairline rules, square corners, mono labels, one electric annotation. What the
site does with grid paper, registration crosses and heavy rules, the console does with
nothing at all: the reader's own infrastructure is the figure on the sheet, and the shell
is there to hold it flat.

Density is the difference from the site. This is an operator's surface read at 14px, on
an 8px-and-under rhythm, with a fixed 56px header rule, a resizable sidebar and pages that
run to the viewport rather than to a prose column. Like the docs, the console runs IBM Plex Sans
for everything set as text, Space Grotesk for headings only, and IBM Plex Mono for
everything the system names rather than says.

The one place the apparatus returns is a canvas. The project overview is an infinite sheet
with a faint dot ground, square hairline tiles laid out left-to-right, and dashed curves
where an app reads a resource. It is a drawing of a deployment, stamped with when it was
true, and it mutates nothing.

**Key Characteristics:**
- Paper and hairlines only. Electric appears on focus rings, the active nav item, a changed value and a drop target, nowhere else.
- Zero radius, enforced by `--radius: 0rem` and explicit square variants on every primitive.
- Depth by hairline and one tonal step to Fog. Floating layers take a zero-blur 3px offset. No glow, no blur.
- Mono uppercase Label type names every section, column, chip and stamp.
- Canvases may carry a faint dot ground; no other surface carries any ground.
- Every view of infrastructure carries its "as at" stamp in Label type.

## Colors

Monochrome paper and ink, one electric annotation, and two status colors that report an
outcome rather than decorate a surface.

### Primary
- **Electric** (`{colors.electric}`, dark `{colors.electric-dark}`): the annotation. Focus rings at 40-50% opacity, the active sidebar item, a changed value's 5% tint and a drop target. It is also `--ring`, `--sidebar-primary` and `--chart-1`, never `--primary`.

### Neutral
- **Ink** (`{colors.ink}`, dark `{colors.ink-dark}`): headings, tile titles, values in a field row, the selected tile's border. Dark ink is warm off-white.
- **Body** (`{colors.body}`, dark `{colors.body-dark}`): descriptions, field names, Label type, and every icon that is not carrying a brand.
- **Steel** (`{colors.steel}`, dark `{colors.steel-dark}`): line weight, not text. Usage edges, ghost borders, hover borders, and the undetected-framework avatar.
- **Fog** (`{colors.fog}`, dark `{colors.fog-dark}`): the one tinted surface. Resource tiles, the sidebar, skeletons, and hover fills.
- **Paper** (`{colors.paper}`, dark `{colors.paper-dark}`): the page and every app tile, chip, control and panel that sits on it.
- **Hairline** (`{colors.hairline}`, dark `{colors.hairline-dark}`): every border, divider, table rule and panel edge.
- **Grid** (`{colors.grid}`, dark `{colors.grid-dark}`): declared for parity with www; the console paints nothing with it.
- **Faint** (`{colors.faint}`, dark `{colors.faint-dark}`): the skipped-outcome dot.
- **Terminal** family (`{colors.terminal}`, `{colors.terminal-rule}`, `{colors.terminal-foreground}`, `{colors.terminal-ink}`): the command pane, dark in both themes.

### Tertiary
- **Go** (`{colors.go}`, dark `{colors.go-dark}`): a succeeded outcome dot and the copied-confirmation check. Only where something actually succeeded.
- **Warn** (`{colors.warn}`, dark `{colors.warn-dark}`): what is owed but not yet wrong — a required value nobody has filled, an incomplete group, an owed tally, a stale override. Carried as text or a 10% fill behind text, never a solid fill.
- **Destructive** (`{colors.destructive}`, dark `{colors.destructive-dark}`): a failed outcome dot, the failure banner's border and text, an app's reported error, a save that conflicted or was refused, and a value that fails its schema. Only where an operation actually failed or a value is actually invalid — an unfilled requirement is Warn, not Destructive.

### Named Rules
**The One Annotation Rule.** Electric owns less than ten percent of any view. On the
console it is a focus ring, the active nav item, a changed value and a drop target. It
never fills a tile, a panel, a banner, a badge, a button, an identity square or a status.

**The Outcome Colour Rule.** Go and Destructive report a record, never a mood. A 8px dot
carries them on a tile; a ghost tile carries no dot at all, because a torn-down thing has
no outcome to report.

**The Same Ink Rule.** The console and the site draw from one token set. A new token on
the console is a new token on the site, so it earns its place in both records first.

## Typography

**Text Font:** IBM Plex Sans (with system-ui)
**Heading Font:** Space Grotesk (with system-ui)
**Label/Mono Font:** IBM Plex Mono (with ui-monospace)
**Wordmark Font:** Archivo 800 (lowercase "ocel" only)

**Character:** The docs register of the site, exactly. Plex Sans sets everything the
console says: table cells, fields, buttons, badges, nav, notices. Space Grotesk sets only
what the console shouts: `h1`, `h2` and `h3`, which is the page title, a promotion id as a
title, and the heading of an empty or error notice. Plex Mono sets everything the console
names in its literal form: commands, keys, paths, urls, bindings. Plex Sans and Plex Mono
are one family, so the terminal pane and the table share bone structure.

**Swapping faces.** Every face is declared once, in `app/fonts.ts`, as a role: `body`,
`heading`, `mono`, `display`. The role owns its CSS variable and the rest of the app only
ever names the role (`font-sans`, `font-heading`, `font-mono`, `font-display`, and the
base rule that puts `h1` to `h3` in the heading face). To try Inter for body and keep
Grotesk for headings, change the `body` loader in that one file and nothing else. To put
Grotesk back everywhere, point `heading` and `body` at the same loader.

### Hierarchy
- **Title** (600, 1.5rem, -0.025em): the page heading in the standard page shell, balanced and held to prose width.
- **Headline** (600, 1.125rem, -0.025em): the heading inside a notice or an empty-state card on a canvas.
- **Subject** (600, 0.875rem): a tile's name, a details-panel heading, and the name of a related app or resource in a list.
- **Body** (400, 0.875rem / 1.25rem): the document default, set on `<body>`. Descriptions, menu items, nav labels.
- **Detail** (400, 0.75rem): panel field rows, table cells, explanatory sentences inside a panel section.
- **Label** (500 mono, 0.6875rem, 0.14em, uppercase): section headings in the details panel, table headers, chips, key pills, ghost captions, the tile's count line, and the provenance stamp. Set in Body colour.
- **Path** (400 mono, 0.75rem): urls, binding names, variable keys, folders, error text and the files in a usage caption. Keeps its own case and natural tracking.
- **Command** (500 mono, 0.8125rem): the environment switcher and the command pane's `$` line. A control that reads as something you could type.

### Named Rules
**The Caption Rule.** Anything that names rather than says is Label type: mono, uppercase,
0.14em, Body colour. The carve-out is a literal string — a path, url, key, binding name or
command — which is Path or Command type and keeps its case exactly as the CLI reported it.

**The Contrast Floor Rule.** Label type is set in Body colour (`{colors.body}`), not Steel.
Steel measures 3.45:1 at 11px, under the floor for text that size; Steel stays a line
colour. Any new small-type role inherits Body, not Steel.

**The Real Text Rule.** Commands, slugs, urls, keys and grants are primary content. They
are selectable text, never an image or a screenshot.

## Layout

The shell is a resizable offcanvas sidebar plus an inset column. The column opens with a
56px header carrying a hairline underline, the sidebar trigger and the project switcher;
everything below it is the page. Standard pages use the page shell: 20px side padding,
32px above, 48px below, 40px side padding from the medium breakpoint, a title block held
to prose width, and 24px between blocks.

A canvas page is the exception and takes the whole remaining viewport,
`calc(100svh - 56px)`, with overflow hidden. It has no title block. The canvas pins two
strips 16px from the top edge: environment switcher and provenance at the left, the fit
and zoom controls at the right, 6px apart.

The service map lays out left-to-right with dagre: apps in the left rank, resources in the
right, 176px between ranks, 24px between siblings, auto-fitted with 12% padding at a
maximum of 1× zoom (8% padding and a 0.75 floor on mobile).

The details panel is 360px wide with a hairline left edge on medium and up. Below the
medium breakpoint it becomes a bottom sheet capped at 70% of the viewport height over a
flat 10% black scrim.

Rhythm is small and tight: 4, 6, 8, 12, 20, 32. Dividers are structural — panel sections,
table rows and the header are separated by a hairline, not by margin.

### Named Rules
**The Quiet Shell Rule.** The console never shows the site's landing apparatus: no grid
paper, no registration crosses, no rotated figure captions, no marquee, no heavy rules.
The shell is paper, hairlines and type so the reader's infrastructure is the only thing
on the sheet.

**The Canvas Dot Rule.** A canvas, and only a canvas, may carry a dot ground: 2px dots in
`{colors.steel}` at 55% on a 24px gap. Every state of that canvas carries the same ground, so the
empty, error and loading screens sit on the same sheet as the map. No other surface in the
console has a ground of any kind.

## Elevation & Depth

Flat by construction. The console uses four depth devices and no others: a hairline
border, one tonal step from Paper to Fog, the dark terminal material of the command pane,
and the float dial on layers that sit above the page — menus, popovers, comboboxes,
selects and dialogs — a 10% Ink ring with a 3px zero-blur offset in Hard Shadow. Nothing lifts on hover; hover darkens a border to Steel or tints a fill to Fog.
Overlays sit on a flat 10% black scrim with no backdrop blur. The canvas library's own
node shadows, outlines and radii are stripped to zero in the global stylesheet.

### Named Rules
**The No Blur Rule.** Nothing casts a soft shadow, glows, or blurs what is behind it. A
floating layer takes the float dial; anything on the page gets a hairline or steps to Fog.

**The Tonal Step Rule.** One step only, and it carries meaning: an app tile is Paper, a
resource tile is Fog. The map reads as two materials — the things you wrote and the things
they read — before a single word is read.

## Shapes

Square everywhere. `--radius` is `0rem`, the whole radius scale derives from it, and every
primitive additionally declares its square corner so a library component cannot arrive
rounded. The only curves in the system are the cut-ring mark, brand marks drawn by their
owners, and the 8px outcome dot.

Borders are 1px hairlines by default. A selected tile steps its border to Ink; a hovered
tile steps it to Steel. A ghost — something that existed and was torn down, or a thing
that does not exist yet — takes a dashed border in Steel at 60%. The failure banner takes
a solid Destructive border.

Usage edges are dashed 1.5px bezier curves in Steel at 60% with no arrowheads, entering
and leaving on invisible left and right handles. An edge that touches the selected tile,
or is hovered, becomes solid Ink at full opacity. Edges never carry arrowheads, weight, or
animation.

## Components

### Buttons
Printed and exact. Flat Ink fill, square, no shadow, and a 1px downward nudge on press.
- **Shape:** square (0px radius)
- **Primary:** Ink fill, Paper text, 32px tall, 10px side padding, 14px medium. Hover drops the fill to 80%.
- **Outline:** Paper fill, Ink text, hairline border. Hover tints to Fog.
- **Ghost:** no border or fill at rest; hover tints to Fog.
- **Destructive:** Destructive text on a 10% Destructive fill. No solid red fill anywhere.
- **Hover / Focus:** focus-visible draws a 1px Electric border plus a 1px Electric ring at 50%; on canvas surfaces a 2px Electric ring at 40%, inset. Never a lift.

### Chips
- **Style:** Paper fill, hairline border, Label type, 2px by 6px. Used for an app's compute and runtime, a variable's class, and a binding's property keys.
- **State:** static. Chips report, they do not toggle.

### Tiles
The map's unit, and the reason the console is a drawing. Tiles never float and never round.
- **Corner Style:** square
- **Background:** Paper for an app, Fog for a resource, Paper for a ghost
- **Border:** hairline at rest, Steel on hover, Ink when selected, dashed Steel at 60% when a ghost
- **Shadow Strategy:** none
- **Internal Padding:** 12px
- **Content:** a mark and the name on the top line with the status at the right; the middle carries chips or the binding name in Path type; the foot carries a Label-type count line ("3 variables · 2 reads"). An app tile that reported a url shows it in Path type with the scheme stripped, underlined on hover.
- **Ghost:** every mark drops to 50% opacity, text goes to Body, and the outcome dot is removed entirely.

### Details Panel
Opens when a tile is selected; 360px, Paper, hairline left edge, its own scroll. A header
row carries the mark, the name, the provider mark or outcome dot, and a close control.
Below it, sections separated by a hairline top edge, each with a Label-type heading and
20px of side padding: urls, runtime, variables, reads — or binding, keys, grants, read by.
Field rows put the name in Body at 12px on the left and the value in Path type on the
right. Escape closes. Below the medium breakpoint the same body renders as a bottom sheet
capped at 70% of viewport height.

### Tables
The list register for runs, on the shadcn Table. A hairline frame, Label-type column
heads at 36px, rows at a fixed 56px so every run reads as one line, hairlines between
rows, hover tints to Fog at 50%. The whole row navigates and the first cell carries the
real link. One colour per row: the status dot (Go for a deployed promotion, Faint for a
teardown, Destructive for a failure). Apps are marks only, with a Destructive dot on a
failed app and nothing on a succeeded one. The environment Badge is outline, except the
production run that is currently live, which takes the Ink fill. Author is an
Avatar with initials; trigger is the terminal mark and the command that ran. Every state
renders the same column heads: empty, error and no-older notices sit inside the body as
one spanning cell.

### Run Detail
A Label-type breadcrumb, the promotion id as a mono Title, then one status line (dot,
word, stamp, environment Badge, tag). Actions sit at the right as outline Buttons; those
the console cannot perform open a Popover holding the command pane and one line on why.
Below that, three blocks 24px apart, each a joined grid: cells share hairlines through
the overview's negative-margin trick, so a block reads as one sheet ruled into panes and
never as cards with gaps. The first block is the summary, nine field cells three across
(name in Detail size over the value) and a full-width mono footer for run id and CLI
version. The second is two panes, Apps and Resources, each with a Label heading and
single-line rows. The third is the shadcn Accordion, one item open at a time: Build logs,
Domains, Checks, each trigger carrying its count or duration in muted Detail beside the
chevron. Build logs is one Terminal-material pane with the stages' lines in order and a
dim `# stage` line where each began; the stage trace is not drawn.

### Badges
The shadcn Badge, square, 20px tall. Outline for every environment except the live
production promotion, which is the one Ink fill on a run page or row.

### Popover
The shadcn Popover, square, its ring and the float offset and nothing more. Holds a command pane behind an
action the console cannot run itself.

### Command Pane
The console's call to action, and the site's terminal in miniature. Dark in both themes:
Terminal fill, Terminal Rule border and title bar, three 10px traffic-light dots
(`#ff5f57`, `#febc2e`, `#28c840`), the prompt in Terminal Foreground, the command in
Terminal Ink, and a copy control that flips to a Go check for 1.5 seconds. Every screen
that cannot act names the command that can, in this pane.

### Provenance Strip
Pinned top-left over the canvas, in Label type on Paper: the relative stamp, then
promotion, tag, provider and region separated by middots. The relative time re-renders
every 30 seconds and carries the absolute time as its title. Under a failure, a
Destructive-bordered banner sits between the switcher and the stamp, clamping the error to
two lines with a Label-type More/Less control.

### Map Controls
Three 32px squares, top-right, 6px apart: fit, zoom in, zoom out. Paper fill, hairline
border, Body icon; hover steps the border to Steel and the icon to Ink.

### Marks
- **Pairing:** the main mark is what the thing *is* (framework, runtime, or resource type) at 20px; the corner mark is the provider service it landed on at 14px. An app carries its framework or runtime mark; a resource carries its type mark plus the provider mark.
- **Sources:** simple-icons for runtimes, providers and databases, rendered at the brand's own hex; local SVGs under `public/frameworks/` for frameworks, with a dark variant swapped by theme when one exists; Phosphor for the generic fallbacks (cube, package, cloud, drives) in Body colour.
- **Never** a letter, an emoji, or a coloured square standing in for a brand that has a mark.

### Navigation
- **Sidebar:** Fog ground, hairline header and footer at 56px and matching the column header, Body-coloured items with a Phosphor icon that fills and turns Electric when active. The project scope shows the slug in mono as its group label, with an "All projects" escape above it. Icons wiggle once on hover. Resizable by a drag handle; state persists in a cookie.
- **Header:** 56px, hairline underline, sidebar trigger plus project switcher. Nothing else.
- **Switchers:** environment in Command type, project and organization as comboboxes with a mono search field; both square, both hairline-bordered.

### Inputs / Fields
- **Style:** hairline border, transparent fill, square, 32px tall, 10px side padding.
- **Focus:** border shifts to Electric with a 1px Electric ring at 50%. No glow.
- **Error:** Destructive border and a 20% Destructive ring.

### Skeletons
Fog rectangles at the tiles' exact sizes, positioned where dagre will put them, pulsing on
the same dot ground the map will use. The loading state is the drawing before the ink.

## Do's and Don'ts

### Do:
- **Do** keep every corner square. `--radius: 0rem` is the invariant, and each primitive restates it so a library component cannot arrive rounded.
- **Do** set every label, table header, caption and stamp in uppercase 11px at 0.14em at weight 500 in Body colour, at one weight. A table's column heads are that label and nothing else, so none of them inherits a `th`'s bold.
- **Do** set that register in **sans**. Mono is not perceivable there: `text-transform: uppercase` removes every `l`/`1`/`I` and `O`/`0` carrier mono exists to disambiguate, and 0.14em tracking has already destroyed the advance rhythm, so the two faces differ by 2% of width and one serifed `I`. A face the reader cannot perceive is cost without benefit. Measured, then changed.
- **Do** reserve mono for the two jobs where a reader does character-by-character work: **a value being proofread or pasted** (the value field, and a revealed value) and **a command to type** (the command pane, an inline `ocel …`). At 13px in Plex Sans `I` and `l` are identical bare stems; in Plex Mono they are not, and that discrimination is the whole reason the font is here.
- **Don't** set an identifier in mono merely because it is one. SCREAMING_SNAKE_CASE already says "literal" and costs nothing to render; a leading `/` says path; a chip says name; `tabular-nums` says digits. Keys, paths, group names, environment names and versions are sans. Mono as a badge of technicality is a costume.
- **Do** draw from one scale: 11px sans label (500), 12px sans meta, 13px sans key (500) and path (400), 13px mono value, 14px sans body, 14px sans subject (600). Title sizes (18/22/34) are a separate register and do not mix into it.
- **Scope, as of this commit:** `ui/vars` (the variables table, in both the console and `ocel env ui`) and the variables page's own stamp follow the rule above. The rest of the console — the deployments table, overview tiles, run detail, the switchers — still sets the label register in mono from `app/(dashboard)/label.ts`. That is a known inconsistency held deliberately to keep the blast radius small; the register is forked in three places and wants collapsing into one token before the family changes console-wide.
- **Do** size an icon from the text beside it — 14px inline with 11–14px text, 16px for a standalone control or mark, 24px for the figure in an overlay or empty state.
- **Do** make the tonal step carry meaning: app tiles Paper, resource tiles Fog.
- **Do** step a border rather than lift a surface: hairline at rest, Steel on hover, Ink when selected.
- **Do** draw relationships as dashed 1.5px Steel beziers at 60% with no arrowheads, going solid Ink only when active.
- **Do** stamp every infrastructure view with its "as at" moment and the promotion it came from.
- **Do** name the command in a dark command pane wherever the console cannot act.
- **Do** confine the dot ground to canvases, and give every state of a canvas the same ground.

### Don't:
- **Don't** let Electric fill anything: tile, panel, banner, chip, badge, button, identity square or status.
- **Don't** add soft shadows, glows, blurs or gradients. Floating layers use the float dial; overlays use a flat 10% black scrim.
- **Don't** bring the site's landing apparatus here: no grid paper, no registration crosses, no rotated figure captions, no marquee, no 1.5px rules.
- **Don't** set Label type in Steel. It fails contrast at 11px; Steel is a line colour.
- **Don't** stand a character in for an icon: no `△` for a warning, no `▸`/`▾` for a caret, no `→` for an arrow. Phosphor draws all three.
- **Don't** put an outcome dot on a ghost tile, or a status colour on anything that has not actually reported one.
- **Don't** introduce a second accent, a chart palette, or a new typeface outside `app/fonts.ts`. Plex Sans, Space Grotesk for headings, Plex Mono, and Archivo for the wordmark are the whole set.
- **Don't** stand in for a brand mark with a letter, an emoji or a coloured square.
- **Don't** render a command, url or record as an image.
