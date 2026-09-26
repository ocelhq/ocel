---
name: Ocel
description: The one theme every Ocel surface renders in. Invariants, register dials, and the named exceptions.
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
  heading:
    fontFamily: "Space Grotesk, IBM Plex Sans, system-ui, sans-serif"
    fontWeight: 600
    letterSpacing: "-0.03em"
  body:
    fontFamily: "IBM Plex Sans, system-ui, sans-serif"
    fontWeight: 400
  label:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontSize: "0.6875rem"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.14em"
  code:
    fontFamily: "IBM Plex Mono, ui-monospace, monospace"
    fontWeight: 400
  wordmark:
    fontFamily: "Archivo, system-ui, sans-serif"
    fontWeight: 800
    letterSpacing: "-0.04em"
rounded:
  none: "0px"
spacing:
  hair: "2px"
  xs: "4px"
  control: "6px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "20px"
  2xl: "24px"
  3xl: "32px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.paper}"
    rounded: "{rounded.none}"
  button-outline:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.none}"
  terminal:
    backgroundColor: "{colors.terminal}"
    textColor: "{colors.terminal-foreground}"
    typography: "{typography.code}"
    rounded: "{rounded.none}"
---

# Design System: Ocel

## Overview

**Creative North Star: "The Engineering Drawing"**

Every Ocel surface is the same drawing: white paper, black ink, hairline rules, one
electric annotation, square corners, and real code as the figure. The landing page, the
docs, the console and `ocel env ui` differ only in how much of the drafting apparatus is
left on the sheet, never in the materials the sheet is drawn with.

The theme is split by one test: **does this cost the reader a step?**

- If it costs nothing, it is identity. It is an **invariant** and holds on every surface.
- If it can cost a step, it is a **dial**. Each register sets it from a fixed set of values.
- Anything else is a **named exception**, listed below. An unlisted exception is a violation.

This file is the whole contract. `www/DESIGN.md` and `console/web/DESIGN.md` describe the
components of their registers and defer to this file on everything here.

## Invariants

Fixed on every surface. No register overrides them.

**The Square Rule.** Every rectangle has a zero radius. Square corners never slow a
reader down, and radius is the first thing that dissolves a product into its component
library's default. A circle is not a rounded rectangle: status dots, terminal
traffic-light dots, and the cut-ring mark are circles by geometry and stay circles.
Nothing larger than 12px is a circle except the mark.

**The No Blur Rule.** Nothing casts a soft shadow, glows, or blurs what is behind it. A
blur conveys no information a hairline does not. Separation is a hairline, one tonal
step to Fog, the dark terminal material, or the float dial's zero-blur offset.

**The One Ink Rule.** One palette, one file: `ui/theme/src/tokens.css`. Light and dark,
status colours, category colours and terminal colours all live there. A token added for
one surface is a token on every surface, so it lands in that file and in this one before
any surface uses it.

**The One Annotation Rule.** Primary actions are Ink. Electric marks, it never fills: link
underlines, focus rings, the current location in navigation, a selection, a changed
value, a drop target, and the mark. `--primary` resolves to Ink so a library component
cannot arrive filled with Electric. A 5 to 10% Electric tint behind annotated text counts
as a mark.

**The Type Set Rule.** Space Grotesk for headings, IBM Plex Sans for running text, IBM Plex
Mono for code, commands and values proofread character by character, Archivo 800 for the
lowercase wordmark. No fifth face.

**The Label Rule.** Anything that names rather than says is set uppercase at 0.6875rem,
500, 0.14em tracking, never in Steel below 14px.

**The Dark Screen Rule.** Terminal and command panes are dark in both themes, with the
command as real, copyable text.

## Registers

A register is chosen by the surface, not the host: `ocel env ui` is Operate even when it
runs alone in a browser tab. The host sets it once, as `data-register` on `<html>`, and
every dial below reads from it.

| Dial | Landing | Read | Operate |
|---|---|---|---|
| Surfaces | ocel.dev landing | ocel.dev docs | console, `ocel env ui`, `ui/*` |
| Rule weight | 1.5px Ink | 1px Hairline | 1px Hairline |
| Float edge | Ink | 10% Ink | 10% Ink |
| Float shadow | 6px zero-blur offset | none | 3px zero-blur offset |
| Running text | 1rem | 1rem | 0.875rem |
| Layout spacing | 8px grid | 8px grid | 4px grid |
| Drafting apparatus | grid paper, crosses, rotated captions, marquee | none | a dot ground on canvases only |
| Electric fill | one stamp per section | none | none |
| Looping motion | allowed, off under reduced motion | none | progress only |

**Why the dials move.** Landing is read once, by someone deciding; the apparatus earns
attention and the print offset makes figures read as figures. Read is read for twenty
minutes; the apparatus would compete with the prose, so it is lifted off and nothing
floats. Operate is scanned daily at density; floating layers must separate from the rows
beneath them, so they keep a third of landing's offset, and nothing loops because a
moving thing in a dashboard reads as a live status.

Inside a control, every register may step by 2px and 6px: an icon's gap to its label, a
chip's padding, a menu's inset, a dense table cell. Between controls and blocks, spacing
stays on the register's grid.

Float edge and float shadow are CSS dials: shared components use `border-float`,
`ring-float` and `shadow-float` and pick up the register they render in. The other dials
are set by each host's own layout.

## Named Exceptions

- **Landing header backdrop blur.** Translucent Paper at 85% so content passes beneath the sticky header. The only blur.
- **Landing hero radial mask.** Fades the grid paper at the edges. The only gradient that paints.
- **Canvas dot ground.** 2px Steel dots at 55% on a 24px gap, on Operate canvases and every state of them.
- **Traffic-light dots.** `#ff5f57`, `#febc2e`, `#28c840` inside terminal title bars only.
- **Category colours.** Amber and Violet colour a category icon (the docs CLI and SDK tabs, a folder glyph). Never text, never a fill.
- **Status tints.** Warn and Destructive may sit as a 10% fill behind their own text.

## Colors

- **Ink / Paper:** the drawing. Dark Ink is warm off-white; dark Paper is a cool near-black.
- **Fog:** the one tinted surface. Dark mode adds a Tile step between Paper and Fog for cards.
- **Body:** running prose and labels. **Steel:** line weight, not small text.
- **Hairline:** every divider, border and table rule.
- **Electric:** the annotation. See the One Annotation Rule.
- **Go / Warn / Destructive:** report a record, never a mood.
- **Hard Shadow:** the colour of the float offset.
- **Terminal family:** the dark screen material.

## Open Decisions

- **Label face.** The docs and landing set labels in Plex Mono. `ui/vars` sets them in Plex Sans, having measured that uppercase at 0.14em erases every glyph Mono exists to disambiguate. One of the two becomes the rule for all registers.
- **Landing running text.** The landing sets running text in Space Grotesk; Read and Operate set it in Plex Sans.
