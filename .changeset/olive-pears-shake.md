---
"ocel": patch
---

Serve the RSC variant of a prerendered App Router page to the requests that ask
for one. Next deletes the flight headers off the live request before it builds
the incremental cache, so the cache handler could not tell an RSC request from a
document one and answered both with the html variant — every client-side
navigation into a prerendered page fell back to a full document reload, losing
client state. The membrane now marks the request before Next runs, and the
handler negotiates the variant off that mark.
