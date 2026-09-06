---
name: Ocel
description: Deploys apps to your own cloud. One theme, two registers.
colors:
  electric: "#1f35ff"
  ink: "#0a0a0a"
  body: "#555555"
  steel: "#8a8a8a"
  fog: "#f1f1f1"
  paper: "#ffffff"
  go: "#1a9e57"
  hairline: "color-mix(in srgb, #0a0a0a 12%, transparent)"
  grid: "#ececec"
  faint: "#c9c9c9"
  terminal: "#16181a"
  terminal-rule: "#2a2d31"
  electric-dark: "#4d68ff"
  ink-dark: "#f2f1ec"
  body-dark: "#9aa0a8"
  steel-dark: "#5f646b"
  fog-dark: "#16181a"
  paper-dark: "#101214"
  go-dark: "#3ecf7a"
  hairline-dark: "#2a2d31"
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
  body:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontSize: "1rem"
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
  section: "80px"
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
---

# Design System: Ocel

## Overview

**Creative North Star: "The Engineering Drawing"**

Ocel's surfaces are drawn, not decorated. White paper, black ink, hairline rules, and
one electric annotation. Every element sits square on the page; nothing is rounded,
lifted, or blurred. Type does the work a drawing's line weights do: a grotesk for the
titles, a humanist sans for the reading, and a mono for every caption, label, and
command. Code and terminal output are the figures on the sheet, so they get the most
careful framing on the page.

One theme, two registers. The landing page is the printed sheet in full: grid paper
showing through, registration crosses in the margins, figure captions running down the
edge, and a heavy 1.5px foreground rule under the header. The docs and the console
dashboard are the same drawing with the scaffolding lifted off: plain paper, hairlines
only, and the accent held back to link underlines and the mark, so nothing competes with
the reader's work. The materials never change between them. Only how much of the
drafting apparatus is left visible.

**Key Characteristics:**
- Paper and ink first. Electric is an annotation, never a surface.
- Zero radius everywhere, enforced globally.
- Depth by rule and tone, never by shadow.
- Mono uppercase labels with wide tracking name every section, column, and tab.
- Real code and real terminal output are the illustrations.
- The landing register may show the grid, crosses, captions, and heavy rules. The docs and dashboard register may not.

## Colors

A monochrome drawing with one saturated line and one confirmation green.

### Primary
- **Electric** (`{colors.electric}`, dark `{colors.electric-dark}`): the annotation. Link underlines in prose, the cut-ring mark, the eyebrow above a section, a single highlighted phrase in a display heading, and focus rings. It also names the Deploy tab in the docs sidebar.

### Neutral
- **Ink** (`{colors.ink}`, dark `{colors.ink-dark}`): headings, strong text, the primary button fill, and the heavy rule. Dark mode ink is warm off-white, not pure white.
- **Body** (`{colors.body}`, dark `{colors.body-dark}`): running prose and descriptions.
- **Steel** (`{colors.steel}`, dark `{colors.steel-dark}`): mono labels, table headers, the docs badge, dashed diagram lines, and terminal prompts.
- **Fog** (`{colors.fog}`, dark `{colors.fog-dark}`): the only tinted surface. Tiles, inline code, chips, and the sidebar in dark mode.
- **Paper** (`{colors.paper}`, dark `{colors.paper-dark}`): the page. Dark paper is a cool near-black with a faint blue cast.
- **Hairline** (`{colors.hairline}`, dark `{colors.hairline-dark}`): every divider, border, and table rule. Twelve percent ink in light; a solid dark grey in dark mode.
- **Grid** (`{colors.grid}`) and **Faint** (`{colors.faint}`): landing register only. The grid paper lines and the quiet registration crosses.
- **Terminal** (`{colors.terminal}`) with **Terminal Rule** (`{colors.terminal-rule}`): terminal panes are always dark regardless of theme. They read as a second material, a screen set into the paper.

### Tertiary
- **Go** (`{colors.go}`, dark `{colors.go-dark}`): the check mark in terminal output and success states. It appears only where something actually succeeded.

### Named Rules
**The One Annotation Rule.** Electric owns less than ten percent of any view. It underlines, marks, and points. It never fills a section, a card, or a button on any surface.

**The Dark Screen Rule.** Terminal and code panes keep their dark palette in light mode. They are screens set into the drawing, not part of the paper.

**The Same Ink Rule.** Landing, docs, and dashboard draw from one token set. A new color on one surface is a new color on all of them, so it must earn a place in this file first.

## Typography

**Display Font:** Space Grotesk (with IBM Plex Sans, system-ui)
**Body Font:** IBM Plex Sans (with system-ui)
**Label/Mono Font:** IBM Plex Mono (with ui-monospace)
**Wordmark Font:** Archivo 800 (lowercase "ocel" only)

**Character:** A draughtsman's set. The grotesk gives titles a slightly mechanical edge with tight negative tracking; the Plex pair keeps prose and code in one family so a page reads as one hand. Mono in small caps with wide tracking is the caption voice, used wherever the system labels something rather than says it.

### Hierarchy
- **Display** (600, `clamp(2.125rem, 5vw, 3.375rem)`, 1.04 to 1.15, -0.035em): the page title in docs at the small end, the landing hero at the large end, and the docs index hero fixed at 2.5rem between them. One phrase inside it may take Electric.
- **Headline** (600, 1.375rem, 1.25, -0.03em): section headings. In docs they carry 2.5rem of space above; the landing sets its section headings larger at 34px with -0.02em.
- **Title** (600, 1.0625rem, 1.4): sub-headings, tile titles, and sidebar navigation, which is set in the display face at 0.875rem.
- **Lede** (400, 1.125rem, 1.55): the one sentence under a display title, in Body color, held to about 34 characters. Nowhere else.
- **Body** (400, 1rem, 1.6): prose in Body color. Max width follows the docs column, about 65 to 75 characters. On the docs index the prose is held to 35rem while figures, tiles, and tabs keep the full 56rem column.
- **Label** (500 mono, 0.6875rem, 0.14em, uppercase): sidebar separators, table of contents heading, table headers, the docs badge, keyboard keys, figure captions, and content tabs. Steel, except tabs, which are controls: Body at rest and Ink when active.
- **Filename** (400 mono, 0.75rem): the title bar of a code block. A path is code, so it keeps its case and its natural tracking.
- **Eyebrow** (mono, 0.75rem, 0.08em): the line above a landing section, in Electric. Landing register only.
- **Code** (400 mono, 0.84375rem, 1.9): code blocks and terminals. Inline code is 0.85em of its surrounding text on Fog.

### Named Rules
**The Caption Rule.** Anything that names rather than says is set in Label: mono, uppercase, wide-tracked, Steel. No sentence case labels, no bold sans labels. The one carve-out is a filename, which is a literal path and stays as written.

**The Code Is Prose Rule.** Code blocks and terminal output are content, never decoration. They get real text, a hairline frame, and the generous 1.9 line height. Never render them as images.

## Layout

The docs use the Fumadocs three-column shell: sidebar, article, and a clerk-style table of contents. The article column is prose width; the docs index widens to 56rem and drops the table of contents, breadcrumb, and footer. The landing holds a 1180px container with 20px side padding on mobile and 40px from the medium breakpoint, and 64px to 84px of vertical padding per section.

Rhythm is on an 8px base. Tiles sit in a responsive grid of at least 16rem columns with 16px gaps. Compared code panes sit side by side from the medium breakpoint and stack below it. Docs section headings take 2.5rem above; the landing register in docs takes 5rem.

Hero layouts split text and figure. In docs the figure is the overview diagram, first on mobile and right of the text on large screens. On the landing the figure is a live terminal in a two-column grid weighted slightly toward the terminal.

Dividers are structural. A hairline under the hero, under table rows, and between compared panes carries the layout; margin alone never does.

### Named Rules
**The Two Registers Rule.** The landing page may show the drafting apparatus: 48px grid paper faded by a radial mask, mono "+" registration crosses in the margins, rotated figure captions along the edge, a 1.5px foreground rule under the header, and a marquee. Docs and the console dashboard show none of it. They keep paper, hairlines, and type so nothing competes with the work at hand.

## Elevation & Depth

No shadows. The system is flat by construction; the docs stylesheet strips every shadow the docs framework ships, including on code blocks and keyboard keys. Depth comes from three things only: a hairline rule, a tonal step from Paper to Fog, and the dark terminal material set into the page. The landing header uses a translucent paper with backdrop blur so content passes beneath it, and that is the only blur in the system.

### Named Rules
**The No Shadow Rule.** Nothing casts a shadow. A surface that needs to separate from the page gets a hairline or steps to Fog. Hover never lifts; it darkens a border or tints a fill.

## Shapes

Square. A global rule sets every radius to zero and marks it important, so even library components arrive with hard corners. The only curve in the system is the cut-ring mark, a circle with a wedge removed, and that is why it reads as a signature.

Borders are hairlines by default. The landing register escalates to a 1.5px Ink border for the outline button and the header rule. Callouts take a 2px left edge in their status color. Prose links draw a 1.5px Electric underline as a bottom border rather than a text decoration, so it sits a touch below the baseline like a ruled annotation.

Diagram connectors are dashed 1.5px Steel curves at 60% opacity, cubic and vertical, with no arrowheads.

## Components

### Buttons
Printed and exact. Flat ink, square, no shadow, no motion beyond a color change.
- **Shape:** square (0px radius)
- **Primary:** Ink fill, Paper text, 1px Ink border, 10px by 16px padding, 0.9375rem medium. The landing sets a larger hero variant at 13px by 24px and 15px semibold.
- **Outline:** Paper fill, Ink text, 1px hairline border in docs, 1.5px Ink border on the landing. Hover tints to Fog. The landing often sets outline buttons in mono for command-like calls such as "ocel init ↵".
- **Hover / Focus:** border darkens to Ink or fill shifts one tonal step. Focus is a 2px Electric ring at 30% opacity. Active nudges down 1px on the console button and nowhere else.
- **Header CTA:** Ink fill, Paper text, 13px semibold, 8px by 16px, hover at 85% opacity.

### Chips
- **Style:** Paper fill, hairline border, mono Label type in Steel, 4px by 6px padding. The docs badge next to the wordmark is the canonical chip.
- **Landing variant:** tool chips with an inline logo, hairline border, hover to Ink border, mono 12.5px in Body.

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
- **Focus:** Electric ring, no glow.
- **Error:** destructive red border and 20% ring on the console; docs have no forms.

### Navigation
- **Docs sidebar:** display face at 0.875rem, separators in Label type, tabs colored per root (Deploy in Electric, CLI in amber, SDK in violet) through the tab icon only. Collapse control centered.
- **Landing header:** sticky, translucent Paper at 85% with backdrop blur, 1.5px Ink rule beneath. Wordmark left, 13px medium links in Body hovering to Ink, an "ALPHA" mono label, a mono theme toggle framed by a hairline, and the Ink CTA.

### Terminal
The signature component. A dark pane set into the page in either theme.
- **Frame:** Terminal fill, Terminal Rule border, a title bar with three 10px traffic-light dots (red `#ff5f57`, amber `#febc2e`, green `#28c840`) separated by a Terminal Rule line.
- **Body:** Code type, Body-dark text, prompt in Steel, command in Ink-dark, check in Go, arrow in Electric, elapsed time in Ink-dark.
- **Landing variant:** interactive. Clicking advances a scripted sequence with a blinking cursor.

### Compare
Two code panes side by side inside one hairline frame, the second pane's title bar hidden so both read as one figure with a diff. Used to show a one-line change in config.

### Callout
Hairline frame, 2px left edge in the callout's status color, icon hidden, square.

### Overview Diagram
An inline SVG figure: framework logos across the top, a terminal in the middle, provider logos along the bottom, joined by dashed Steel curves. Logos are fitted to a 38px box preserving their ratio.

## Do's and Don'ts

### Do:
- **Do** keep every corner square. The global zero-radius rule is the invariant, not a default.
- **Do** set every label, caption, table header, and badge in mono uppercase at 0.6875rem with 0.14em tracking in Steel.
- **Do** underline prose links with a 1.5px Electric bottom border and Ink text.
- **Do** keep terminal and code panes dark in both themes, framed by a hairline in docs.
- **Do** separate with hairlines and tonal steps to Fog. Hover darkens a border or tints a fill.
- **Do** confine grid paper, registration crosses, figure captions, and 1.5px rules to the landing register.
- **Do** use the cut-ring mark and lowercase Archivo wordmark exactly as built. It is the one curve in the system.

### Don't:
- **Don't** let Electric fill a button, card, section, or background on any surface. Under ten percent, as annotation only.
- **Don't** add shadows, glows, blurs, or gradients. The header's backdrop blur and the hero's radial grid mask are the two exceptions, both landing only.
- **Don't** bring the landing's drafting apparatus into docs or the dashboard. Those surfaces stay quiet so the work is the only thing that stands out.
- **Don't** invent a second accent. Amber and violet exist only as sidebar tab colors; they never appear in content.
- **Don't** render code or terminal output as images or screenshots.
- **Don't** set headings, buttons, or labels in a fourth typeface. Grotesk, Plex Sans, Plex Mono, and Archivo for the wordmark are the whole set.
