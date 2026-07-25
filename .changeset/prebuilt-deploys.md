---
"ocel": minor
---

Add `ocel build`, which builds the project's apps into `.ocel/output` without deploying and needs neither a login nor a configured provider, and a `--prebuilt` flag on `ocel deploy`, `ocel preview` and `ocel preview up` that deploys that existing output instead of building the apps again.
