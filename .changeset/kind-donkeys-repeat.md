---
"ocel": minor
---

Deliver a `sensitive`-class variable as ciphertext inside the deployed bundle.
The values are sealed with AES-256-GCM under a data key drawn fresh for each
deploy, and that key travels in the function's configuration, so the values
themselves never appear there and the membrane opens them locally as it starts
the application process — no credentials, no client and no call on the init
path. They are injected under a namespaced name, and a bundle that cannot be
opened fails init with a diagnosable error rather than serving empty values.
Reading one stays a plain synchronous property access, identical to every other
class.

What this protects against is real but narrow: configuration-only viewers,
console screenshots, environment dumps in logs, and values pasted into a
support thread. It is not separation against an attacker who can already read
the function — on AWS a single `GetFunction` returns the environment and a
download link to the code artifact together, so whoever can read the key can
read the ciphertext it opens. Values in the variable store stay encrypted at
rest under the substrate's own KMS key; this is about what a deployed
function's configuration discloses, not about how the values are stored.

A variable whose class this deploy path cannot deliver — an unrecognised one
from a newer client — now fails the deploy naming the variable, rather than
being dropped and leaving the value absent at runtime. A deployed
app is also told the folder it binds, so a read outside a variable's scope
names the real binding.
