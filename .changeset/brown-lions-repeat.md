---
"ocel": minor
---

Add `ocel env ui`: the required-cell matrix in a browser, shipped inside the CLI
binary. A row per variable your code declares, a column per folder, and a socket
per cell — filled, owed, an optional override, or hatched because a scoped
variable holds no value there and nothing would read one. Forbidden cells are
drawn unfillable rather than refused on save, and a per-app readout shows which
apps resolve completely.

The page is served over loopback from the provider session the command already
holds, so it needs no hosted service and no network beyond the provider's own
calls. Its session token rides the launch URL's fragment and is required on
every API request, alongside origin and host checks on each one.
