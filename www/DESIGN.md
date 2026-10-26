---
name: Ocel
description: The landing and read registers of Ocel's design system.
colors:
  electric: "oklch(0.4924 0.2858 266.52)"
  electric-dark: "oklch(0.587 0.225 270.16)"
  ink: "oklch(0.1448 0 0)"
  ink-dark: "oklch(0.9575 0.0067 97.35)"
  paper: "oklch(1 0 0)"
  paper-dark: "oklch(0.1809 0.0052 248.12)"
  fog: "oklch(0.9581 0 0)"
  fog-dark: "oklch(0.2513 0.0057 248.07)"
  tile-dark: "oklch(0.2077 0.005 248.07)"
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
  hard-shadow: "#e8e8e8"
  hard-shadow-dark: "#000000"
  go: "#1a9e57"
  go-dark: "#3ecf7a"
  warn: "oklch(0.5423 0.1315 66.11)"
  warn-dark: "oklch(0.8 0.132 76.5)"
  destructive: "oklch(0.577 0.245 27.33)"
  destructive-dark: "oklch(0.627 0.22 25)"
  amber: "#e8a33d"
  amber-dark: "#e0a44a"
  violet: "oklch(0.606 0.25 292.72)"
  violet-dark: "oklch(0.702 0.183 293.54)"
  terminal: "#16181a"
  terminal-rule: "#2a2d31"
  terminal-foreground: "#9aa0a8"
  terminal-ink: "#f2f1ec"
typography:
  display:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "clamp(2.125rem, 5vw, 3.375rem)"
    fontWeight: 600
    lineHeight: 1.04
    letterSpacing: "-0.035em"
  headline:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "1.375rem"
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.03em"
  title:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "1.0625rem"
    fontWeight: 600
    lineHeight: 1.4
    letterSpacing: "normal"
  lede:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "normal"
  sheet-heading:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "clamp(1.875rem, 3.6vw, 2.75rem)"
    fontWeight: 600
    lineHeight: 1.06
    letterSpacing: "-0.03em"
  statement:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "clamp(2.375rem, 4.4vw, 3.5rem)"
    fontWeight: 600
    lineHeight: 1.05
    letterSpacing: "-0.035em"
  index:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontSize: "2.5rem"
    fontWeight: 600
    lineHeight: 1.1
    letterSpacing: "-0.035em"
  body:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "normal"
  small:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 400
    lineHeight: 1.6
    letterSpacing: "normal"
  label:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.6875rem"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.14em"
  filename:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1
    letterSpacing: "normal"
  code:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.84375rem"
    fontWeight: 400
    lineHeight: 1.9
    letterSpacing: "normal"
  wordmark:
    fontFamily: "Archivo, system-ui, sans-serif"
    fontSize: "1.475rem"
    fontWeight: 800
    lineHeight: 1
    letterSpacing: "-0.04em"
rounded:
  none: "0px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
  2xl: "48px"
  section: "96px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    padding: "10px 16px"
  button-outline:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "10px 16px"
  button-outline-hover:
    backgroundColor: "{colors.fog}"
    textColor: "{colors.ink}"
  tile:
    backgroundColor: "{colors.fog}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "20px"
  chip:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.steel}"
    typography: "{typography.label}"
    rounded: "{rounded.none}"
    padding: "4px 6px"
  inline-code:
    backgroundColor: "{colors.fog}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "0.1em 0.4em"
  terminal:
    backgroundColor: "{colors.terminal}"
    textColor: "{colors.body-dark}"
    typography: "{typography.code}"
    rounded: "{rounded.none}"
    padding: "12px 16px"
  site-bar-end-cell:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "0 16px"
  site-bar-end-cell-hover:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
  copy-command:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
    padding: "10px 12px"
  status-badge:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.electric}"
    rounded: "{rounded.none}"
    padding: "2px 6px"
  target-tab-active:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
    padding: "10px 12px"
  target-tab:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.body}"
    rounded: "{rounded.none}"
    padding: "10px 12px"
---

# Design System: Ocel

Invariants, tokens, register dials and named exceptions live in `@ocelhq/theme`'s
[`DESIGN.md`](https://github.com/ocelhq/console/blob/main/packages/theme/DESIGN.md).
This file covers the landing and read registers' components, and defers to that file
wherever the two disagree.

## Overview

**Creative North Star: "The Engineering Drawing"**

Ocel's surfaces are drawn, not decorated. White paper, black ink, hairline rules, and
one electric annotation. Every element sits square on the page; nothing is rounded or
blurred. Type does the work a drawing's line weights do: a grotesk for the
titles, a humanist sans for the reading, and a mono for every caption, label, and
command. Code and terminal output are the figures on the sheet, so they get the most
careful framing on the page.

One theme, two registers. The landing page is the printed sheet in full: grid paper
showing through the hero, registration crosses in each sheet's corners, a mono caption
under every figure, and heavy 1.5px Ink rules under the header and between sheets. The docs and the console
dashboard are the same drawing with the scaffolding lifted off: plain paper, hairlines
only, and the accent limited to link underlines and the mark, so nothing competes with
the reader's work. The materials never change between them. Only how much of the
drafting apparatus is left visible.

**Key Characteristics:**
- Paper and ink first. Electric is an annotation, never a surface.
- Zero radius everywhere, enforced globally.
- Depth by rule and tone. Only landing figures lift, on a zero-blur print offset.
- Mono uppercase labels with wide tracking name every section, column, and tab.
- Real code and real terminal output are the illustrations.
- The landing register may show the grid, crosses, figure captions, and heavy rules. The docs and dashboard register may not.

## Colors

A monochrome drawing with one saturated line and one confirmation green.

### Primary
- **Electric** (`{colors.electric}`, dark `{colors.electric-dark}`): the annotation. Link underlines in prose, the cut-ring mark, a single underlined phrase in a display heading, the outlined status badge, and focus rings. It also names the Deploy tab in the docs sidebar.

### Neutral
- **Ink** (`{colors.ink}`, dark `{colors.ink-dark}`): headings, strong text, the primary button fill, and the heavy rule. Dark mode ink is warm off-white, not pure white.
- **Body** (`{colors.body}`, dark `{colors.body-dark}`): running prose and descriptions.
- **Steel** (`{colors.steel}`, dark `{colors.steel-dark}`): mono labels, table headers, and dashed diagram lines in docs. The landing sets its labels and captions in Body instead.
- **Fog** (`{colors.fog}`, dark `{colors.fog-dark}`): the only tinted surface. Tiles, inline code, chips, and the sidebar in dark mode.
- **Paper** (`{colors.paper}`, dark `{colors.paper-dark}`): the page. Dark paper is a cool near-black with a faint blue cast.
- **Hairline** (`{colors.hairline}`, dark `{colors.hairline-dark}`): every divider, border, and table rule. Twelve percent ink in light; a solid dark grey in dark mode.
- **Grid** (`{colors.grid}`) and **Faint** (`{colors.faint}`): landing register only. The grid paper lines and the quiet registration crosses.
- **Terminal** (`{colors.terminal}`) with **Terminal Rule** (`{colors.terminal-rule}`), **Terminal Foreground** (`{colors.terminal-foreground}`) and **Terminal Ink** (`{colors.terminal-ink}`): terminal panes are always dark regardless of theme. They read as a second material, a screen set into the paper. Terminal Foreground carries muted output, file titles and captions inside a screen; Terminal Ink carries commands and emphasis.

### Tertiary
- **Go** (`{colors.go}`, dark `{colors.go-dark}`): the check mark in terminal output and success states. It appears only where something actually succeeded.

### Named Rules
**The One Annotation Rule.** Electric owns less than ten percent of any view. It underlines, outlines, marks, and points. It never fills a card or a button on any surface. The landing register's one Electric fill is the terminal's block cursor, at most one per sheet; every other landing Electric is a line: a link underline, the hero phrase's underline, the target select's border, the status badge's outline, the first rule of a definition list.

**The Dark Screen Rule.** Terminal and code panes keep their dark palette in light mode. They are screens set into the drawing, not part of the paper.

**The Same Ink Rule.** Landing, docs, and dashboard draw from `@ocelhq/theme/tokens.css`. A new color on one surface is a new color on all of them, so it earns a place there first.

## Typography

**Display Font:** Space Grotesk (with IBM Plex Sans, system-ui)
**Body Font:** IBM Plex Sans (with system-ui)
**Label/Mono Font:** IBM Plex Mono (with ui-monospace)
**Wordmark Font:** Archivo 800 (lowercase "ocel" only)

**Character:** A draughtsman's set. The grotesk gives titles a slightly mechanical edge with tight negative tracking; the Plex pair keeps prose and code in one family so a page reads as one hand. Mono in small caps with wide tracking is the caption voice, used wherever the system labels something rather than says it. The landing loads no Plex Sans and sets its running text in Space Grotesk; the theme file carries that as an open decision, so this file records it without ruling on it.

### Hierarchy
- **Display** (600, `clamp(2.125rem, 5vw, 3.375rem)`, 1.04 to 1.15, -0.035em): the page title in docs at the small end and the docs index hero fixed at 2.5rem. One phrase inside it may take an Electric underline. The landing hero is a recorded adaptation (see `.impeccable/surfaces/app-landing-page-tsx.md`, ADAPTATIONS): it runs to 4.25rem (`clamp(2.5rem, 5.4vw, 4.25rem)`, 1.02, -0.04em) across 8 of 12 columns, and its one Electric phrase takes a 5px underline at a 10px offset.
- **Headline** (600, 1.375rem, 1.25, -0.03em): docs section headings, with 2.5rem of space above.
- **Sheet Heading** (600, `clamp(1.875rem, 3.6vw, 2.75rem)`, 1.06, -0.03em): each landing sheet's heading, across 7 of 12 columns with its lede beside it in the other 5. Landing register only.
- **Title** (600, 1.0625rem, 1.4): sub-headings, tile titles, and sidebar navigation, which is set in the display face at 0.875rem.
- **Lede** (400, 1.125rem, 1.55): the one sentence under a display title, in Body color. Nowhere else. On the landing the hero lede is 17px at 1.55 capped at 40ch, and a sheet lede is 16px at 1.6 capped at 46ch.
- **Statement** (600, `clamp(2.375rem, 4.4vw, 3.5rem)`, 1.05, -0.035em, about 18ch): the landing's closing call above the second title block. Landing register only.
- **Index** (600, 2.5rem, 1.1, -0.035em): the docs index and comparison page titles.
- **Small** (400, 0.9375rem, 1.6): docs table cells and the compare panes. Landing running text inside cells, tables and lists sits at 14px to 14.5px.
- **Body** (400, 1rem, 1.6): prose in Body color. Max width follows the docs column, about 65 to 75 characters. On the docs index the prose is capped at 35rem while figures, tiles, and tabs keep the full 56rem column.
- **Label** (500 mono, 0.6875rem, 0.14em, uppercase): sidebar separators, table of contents heading, table headers, the revision tag, keyboard keys, figure captions, title-block cell labels, the site bar's Engine and Cloud switch, and content tabs. Steel in docs, Body on the landing; tabs are controls, Body at rest and Ink when active.
- **Filename** (400 mono, 0.75rem): the title bar of a code block. A path is code, so it keeps its case and its natural tracking.
- **Code** (400 mono, 0.84375rem, 1.9): code blocks and terminals. Inline code is 0.85em of its surrounding text on Fog.

### Named Rules
**The Caption Rule.** Anything that names rather than says is set in Label: mono, uppercase, wide-tracked, Steel. No sentence case labels, no bold sans labels. The one carve-out is a filename, which is a literal path and stays as written.

**The Code Is Prose Rule.** Code blocks and terminal output are content, never decoration. They get real text, a hairline frame, and the generous 1.9 line height. Never render them as images.

## Layout

The docs use the Fumadocs three-column shell: sidebar, article, and a clerk-style table of contents. The article column is prose width; the docs index widens to 56rem and drops the table of contents, breadcrumb, and footer. The landing uses a 1180px container with 20px side padding on mobile and 40px from the medium breakpoint, on a 12-column grid from the medium breakpoint. Each sheet takes 64px of vertical padding, 96px from the medium breakpoint.

Rhythm is on an 8px base. Tiles sit in a responsive grid of at least 16rem columns with 16px gaps. Compared code panes sit side by side from the medium breakpoint and stack below it. Docs section headings take 2.5rem above; the landing register in docs takes 5rem.

In docs the hero splits text and figure: the overview diagram, first on mobile and right of the text on large screens. The landing stacks instead: headline and lede share one row (8 and 4 columns), the full-width terminal screen sits beneath them, then its caption, then the title block. Inside the hero screen, code takes 5 columns and the session 7; below the medium breakpoint the session comes first and the second code file is hidden. Sheets pair a figure with text or a second figure at 7 and 5 (or 5 and 7) columns from the large breakpoint, stacking below it.

The landing is a run of sheets. Each sheet opens on a 1.5px Ink rule across the full width, carries a mono "+" registration cross in each corner (13px, Faint, from the medium breakpoint), and alternates Paper and Fog. Only the hero shows grid paper.

Dividers are structural. A hairline under the hero, under table rows, and between compared panes structures the layout; margin alone never does.

### Named Rules
**The Two Registers Rule.** The landing page may show the drafting apparatus: 48px grid paper faded by a radial mask behind the hero, mono "+" registration crosses in each sheet's corners, a visible mono caption under each figure, and 1.5px Ink rules under the header, between sheets, and above the footer. Docs and the console dashboard show none of it. They keep paper, hairlines, and type so nothing competes with the work at hand.

## Elevation & Depth

No blurred shadows. The docs stylesheet strips every shadow the docs framework ships, including on code blocks and keyboard keys, and the read register sets the float shadow to none. The landing register frames its terminal screens and the config diff with a 1.5px Ink border and the float shadow (`shadow-float`): a 6px zero-blur offset in Hard Shadow. The title block and the targets table take the 1.5px Ink border without the offset. Depth in docs comes from three things only: a hairline rule, a tonal step from Paper to Fog, and the dark terminal material set into the page. The landing header uses a translucent paper with backdrop blur so content passes beneath it, and that is the only blur in the system.

### Named Rules
**The No Blur Rule.** Nothing casts a soft shadow. A surface that needs to separate from the page gets a hairline or steps to Fog; a landing figure takes the float offset. Hover never lifts; it darkens a border or tints a fill.

## Shapes

Square. A global rule sets every radius to zero and marks it important, so even library components arrive with hard corners. The only curve in the system is the cut-ring mark, a circle with a wedge removed, and that is why it reads as a signature.

Borders are hairlines by default. The landing register escalates to a 1.5px Ink border for figures, the title block, the targets table, the email field, the copy command, the sign-in button, and the sheet rules; hairlines stay inside them, between cells and rows. Callouts take a 2px left edge in their status color. Prose links draw a 1.5px Electric underline as a bottom border rather than a text decoration, so it sits a touch below the baseline like a ruled annotation.

Diagram connectors are dashed 1.5px Steel curves at 60% opacity, cubic and vertical, with no arrowheads.

## Components

### Buttons
Printed and exact. Flat ink, square, no shadow, no motion beyond a color change.
- **Shape:** square (0px radius)
- **Primary:** Ink fill, Paper text, 1px Ink border, 10px by 16px padding, 0.9375rem medium. On the landing the primary is the early-access submit: Ink fill set flush inside the email field's 1.5px frame, 14px semibold, 16px side padding, hover at 85% opacity, 60% while sending.
- **Outline:** Paper fill, Ink text, 1px hairline border in docs, 1.5px Ink border on the landing. Hover tints to Fog.
- **Hover / Focus:** border darkens to Ink or fill shifts one tonal step. Focus is a 2px Electric ring at 30% opacity in docs; the landing draws a 2px Electric outline at 60% with a 2px offset. Active nudges down 1px on the console button and nowhere else.

### Chips
- **Style:** Paper fill, hairline border, mono Label type in Steel, 4px by 6px padding.
- **Status badge (landing):** a 1px Electric outline, Electric text, mono 10.5px medium uppercase at 0.14em, 2px by 6px, beside a target name. Marks Ocel Cloud's early access; it is an outline, never a fill.

### Tiles
Cards are tiles, not cards. They never float.
- **Corner Style:** square
- **Background:** Fog
- **Shadow Strategy:** none
- **Border:** hairline, hover to Steel
- **Internal Padding:** 20px
- **Text:** title in Ink, description in Body. Start cards add a heroicon outline glyph.

### Inputs / Fields
- **Style:** hairline border, Paper background, square. The docs search trigger is restyled to sit on Paper, and on a slightly lifted dark grey in dark mode.
- **Landing email field:** a 1.5px Ink frame holding a mono 13px input (10px by 12px) and the Ink submit flush right. Focus rings the whole frame in Electric at 40%. Errors print beneath in Destructive at 13px; success replaces the form with a status line naming the address in mono.
- **Focus:** Electric ring, no glow.
- **Error:** destructive red border and 20% ring on the console.

### Copy Command
A command as a button. A 1.5px Ink frame on Paper, mono 13px, 10px by 12px, a Body "$ " prompt that does not select, and a Label "COPY" on the right that reads "COPIED" for 1.6s. Hover tints to Fog.

### Navigation
- **Docs sidebar:** display face at 0.875rem, separators in Label type, tabs colored per root (Deploy in Electric, CLI in Amber, SDK in Violet) through the tab icon only. Collapse control centered.
- **Site bar** (`components/site-bar.tsx`): the engine's own bar; ocelhq.com draws its own from the same theme and mark. A ruled strip, sticky, 56px tall, translucent Paper at 85% with backdrop blur, a 1.5px Ink rule beneath, on the 1180px container. Every item is a full-height cell, so labels share one centre line. Left: the Electric cut-ring mark, the lowercase Archivo wordmark, and the revision tag. Right: Docs (from sm) and GitHub (the GitHub glyph, with its name from md, hidden below 380px) in Space Grotesk 14px medium Body hovering to Ink; then "Get started", the end cell, to the quick start: closed by 1.5px Ink rules on both sides, 14px medium Ink with a drawn arrow from sm, filling with Ink and turning Paper on hover. Focus draws the 2px Electric outline inset 3px inside the cell. No Sign in and no link to ocelhq.com: Ocel Cloud is met in the quick start and the targets.
- **Revision tag:** the release stage beside the wordmark, drawn like a drawing's revision box. A 1px Ink border, Ink mono 11px medium at 0.14em, uppercase, 5px sides. "ALPHA" in the site bar and the docs lockup (`components/docs/logo.tsx`). No fill.
- **Landing footer:** 1.5px Ink rule above, the wordmark at 15px, then 13px Body links hovering to Ink, Ocel Cloud pushed right.

### Terminal (Screen and Session)
The signature component. A dark screen set into the page in either theme.
- **Frame:** a figure on Terminal fill. In docs a Terminal Rule border; on the landing a 1.5px Ink border and the 6px float offset. A title bar at least 44px tall: three 10px traffic-light dots (red `#ff5f57`, amber `#febc2e`, green `#28c840`), the title in mono 11px Terminal Foreground as the figure's caption, and an optional control pushed right. A Terminal Rule line closes the bar, and Terminal Rule lines divide panes inside the screen.
- **Body:** mono at 12.5px (13px from sm), 1.85 line height, wrapping. Transcript tones: command and emphasis in Terminal Ink, muted output in Terminal Foreground, success marks in Go (dark), warnings in Warn (dark), URLs in Terminal Ink with a 1.5px Electric (dark) underline at a 4px offset.
- **Session (landing):** lines print in sequence once the screen is a third in view (55ms per line, 260ms before a prompt), and print whole under reduced motion. Each line holds its height so nothing reflows. When the transcript ends, a prompt and an Electric block cursor blink in steps at 1.1s; the cursor is the landing register's Electric stamp and stops blinking under reduced motion.
- **Code panes:** Shiki output on the screen's own fill, 13px at 1.85, each under a mono 11px filename bar.

### Target Selector
One target state drives the landing. Choosing a target in any selector re-runs every target-aware session and is remembered across visits.
- **Select:** sits in a terminal title bar, right-aligned. A "TARGET" Label (from sm), then a native select with a 1px Electric border on Terminal fill, mono 12px Terminal Ink, and a 1.5px chevron drawn in SVG.
- **Needs line:** a footer row inside the hero screen under a Terminal Rule, mono 12px. A "NEEDS" Label, what the target needs in Terminal Ink, and why in Terminal Foreground; for Ocel Cloud it ends in a "Request one" link that focuses the early-access field.
- **Config diff:** a radio group of target tabs above an `ocel.json` screen. Tabs are mono 12px, 10px by 12px, framed in 1.5px Ink with hairlines between, two per row below sm and one row from it; the checked tab is Ink fill with Paper text, the rest Paper with Body text hovering to Ink. The pane beneath has a filename caption with a Label status on the right, and marks the added provider line with a Go "+" over a 10% Go tint.

### Title Block
The landing's ruled call to action, drawn like a drawing's title block. A 1.5px Ink frame on Paper with three cells on 5, 4 and 3 columns, split by hairlines and stacked below md. Each cell (20px, 24px from md) opens on a Label, then a 14px Body sentence, then its control: the early-access email field, the copy command, the source link with an Electric underline and its licence Label. It closes the hero and the page.

### Figure Caption
Under every landing figure, 16px below it: mono 11px uppercase at 0.14em and 1.6 line height in Body, led by a 24px 1.5px Ink leader rule. It numbers the figure and says what is abridged or illustrative.

### Targets Table
A 1.5px Ink frame on Paper. From md, a table with Label headers and hairline rows: target name in semibold Ink with its status badge, where it runs in Body, what you bring in mono 13px Ink. Below md the same rows become a definition list, one block per target. A closing row of Body text carries an Electric-underlined link.

### Compare
Two code panes side by side inside one hairline frame, the second pane's title bar hidden so both read as one figure with a diff. Used to show a one-line change in config.

### Callout
Hairline frame, 2px left edge in the callout's status color, icon hidden, square.

### Overview Diagram
An inline SVG figure: framework logos across the top, a terminal in the middle, provider logos along the bottom, joined by dashed Steel curves. Logos are fitted to a 38px box preserving their ratio.

## Do's and Don'ts

### Do:
- **Do** keep every corner square. The global zero-radius rule is the invariant, not a default.
- **Do** set every label, caption, table header, and badge in mono uppercase at 0.6875rem with 0.14em tracking, Steel in docs and Body on the landing.
- **Do** underline prose links with a 1.5px Electric bottom border and Ink text.
- **Do** keep terminal and code panes dark in both themes, framed by a hairline in docs and by 1.5px Ink on the landing.
- **Do** separate with hairlines and tonal steps to Fog. Hover darkens a border or tints a fill.
- **Do** confine grid paper, registration crosses, figure captions, and 1.5px rules to the landing register.
- **Do** caption every landing figure beneath it, and say when its output is abridged.
- **Do** use the cut-ring mark and lowercase Archivo wordmark exactly as built. It is the one curve in the system.

### Don't:
- **Don't** let Electric fill a button, card, or background on any surface. Under ten percent, as annotation only, plus the landing terminal's block cursor.
- **Don't** add blurred shadows, glows, blurs, or gradients. The site bar's backdrop blur and the hero's radial grid mask are the two exceptions, both landing only.
- **Don't** bring the landing's drafting apparatus into docs or the dashboard. Those surfaces stay quiet so the work is the only thing that stands out.
- **Don't** invent a second accent. Amber and Violet colour category icons only; they never appear in content.
- **Don't** render code or terminal output as images or screenshots.
- **Don't** set headings, buttons, or labels in a fourth typeface. Grotesk, Plex Sans, Plex Mono, and Archivo for the wordmark are the whole set.
