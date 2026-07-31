---
"ocel": minor
---

Deliver a `secret`-class variable live, from the variable store, at runtime.

Nothing about the value is in the artifact. What the deploy packages is its
address — the store, and the coordinate each declared key resolved to — in a
manifest file inside every one of the app's function packages, so it costs the
function's 4KB configuration budget nothing. Possession of the code discloses
where a value lives, not what it is, and rotating one takes effect with no
redeploy at all.

The membrane is the only component that talks to the store. As init begins it
starts one query and one decrypt per key, concurrently with spawning the
application process, so the spawn never waits on the fetch. The resolved values
are pushed to the application over the control socket the membrane and the
runtime already share, and reads stay plain synchronous property access —
reclassifying a key edits no call site. The runtime is told at spawn which keys
to expect and holds the application's import until the first push has landed,
so every read resolves correctly, including one written at module scope. A
function that cannot resolve a value it declared fails init loudly rather than
coming up with the value absent, and it fails with what the store said: the
runtime is holding its import for a push that will never arrive, so the startup
timeout that would otherwise be reported names nothing. A function that declares
none is told nothing,
waits for nothing, builds no client, resolves no credentials and makes no call,
so a store outage reaches only the functions that actually read the store.

A rotated value is picked up without a redeploy. Each push carries a
generation, and a resolved value is memoised against the generation it came
from rather than forever, so a read through the object sees the rotation. The
bound is per property read: `const key = env.SECRET` at module scope copies the
string out, and nothing can revisit that copy afterwards.

Staleness is bounded at sixty seconds, one bound for the whole project rather
than one per variable, because rotation latency is a project-wide operational
property. Past the bound the next invocation starts a refresh in the background
and goes on being served the value it already has, so no request ever blocks on
a refresh after the first resolution; a refresh that fails changes nothing and
the last resolved values keep serving. The bound is read when an invocation
arrives rather than on a timer, because the platform freezes the sandbox in
between and a timer there does not reliably fire. It is not user-configurable
yet: changing it is a redeploy, unlike the values themselves.

Every live value is checked against its schema at the declaration, before the
application serves anything, because it is the only class whose value can have
drifted since the deploy that shipped the code reading it. A value that fails
fails init, loudly, with the schema's own words withheld the way they are for
every confidential class.

The execution role of an app that declares a live value is granted read on the
variable table, conditioned on that project's own partition in its own
environment class, and decrypt on that class's key alone. An app that declares
none is granted neither.

`ocel dev` resolves live values once, at startup, and says so on the runs that
have them. There is no membrane in a dev run and so no refresh: deployed, a
rotated value is picked up within the bound with no restart; in dev, restart
`ocel dev` to pick one up. Timing is the whole difference — the call site is
identical.
