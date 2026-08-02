---
"ocel": patch
---

A variable problem reported during `ocel dev` now names the file the declaration
was written in, the way `ocel deploy` already did. Dev ran discovery without
source maps, so every diagnostic pointed at the generated `.ocel/entry.mjs`
bundle instead. Both commands now start the discovery process through one
spawner, so the two cannot report differently again.
