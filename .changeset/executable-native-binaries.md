---
"ocel": patch
---

Ship the prebuilt Go binaries with their executable bit intact. `pnpm pack` drops the mode of any file a package does not declare in `bin`, so the published platform packages carried non-executable binaries and running the CLI or a provider failed with `permission denied`.
