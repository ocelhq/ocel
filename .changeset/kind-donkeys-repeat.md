---
"ocel": patch
---

Deliver a `sensitive`-class variable as ciphertext inside the deployed bundle.
The values are sealed under a per-deploy data key wrapped by the substrate's
own key, so a function's configuration holds only that wrapped key and
discloses nothing; the membrane opens them locally as it starts the application
process and injects them under a namespaced name, and a bundle that cannot be
opened fails init with a diagnosable error rather than serving empty values.
Reading one stays a plain synchronous property access, identical to every other
class. A deployed app is also told the folder it binds, so a read outside a
variable's scope names the real binding.
