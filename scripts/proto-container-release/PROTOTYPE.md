# Container release loop, end to end

Throwaway for #590. Never merged. Bash, not Go, because the question is whether the
mechanics the map agreed compose — not how they factor.

## Run it

```
scripts/incus.sh create protobox
eval "$(scripts/incus.sh info protobox)"
scripts/proto-container-release/box-guarantee.sh
OCEL_PROTO_PROXY=kamal scripts/proto-container-release/run.sh
OCEL_PROTO_PROXY=kamal scripts/proto-container-release/drain-test.sh
scripts/incus.sh destroy protobox
```

`OCEL_PROTO_PROXY` is `kamal` or `caddy`. Without a reachable incus socket,
`standin-box.sh up` prints the same `OCEL_INCUS_*` quadruple over a privileged
`docker:28-dind` container with `ocel-deploy` in the `docker` group. Lower fidelity:
alpine, no systemd, no cloud-init, single host network.

`run.sh` walks the whole journey and prints what surprised. `drain-test.sh` isolates
the one property #588 chose kamal-proxy for: whether the flip waits for in-flight
requests before the old container is stopped.

## What surprised

**A locally built image has no digest.** `RepoDigests` is empty until something is
pushed to a registry, so #574's "digest-derived tag" can only mean the config image
ID on the direct-load path. Two boxes loading the same tar agree on it, so it works
as a coordinate — but it is not the manifest digest a registry would report for the
same image, and the two paths therefore name the same build differently.

**Caddy drops in-flight requests; kamal-proxy does not.** Held a request open for 5s
and flipped underneath it. kamal-proxy returned 200 — `deploy` blocks through the
drain, so stopping the old container the instant it returns is safe. Caddy returned
502. This is #588's finding reproduced with working code, on the exact axis #586
flagged for re-weighing at the implementation leg.

**kamal-proxy's drain is bounded.** A request held 15s against `--drain-timeout 10s`
came back 504. Drain is a window, not a promise; the contract owes a stated ceiling
rather than "in-flight requests complete".

**"Any HTTP response" and the proxy's own gate disagree.** An app that 404s its root
passes #586's health gate, then kamal-proxy refuses the flip — "target failed to
become healthy within configured timeout (30s)" — because its check hardcodes 200.
Caddy accepts it, because Caddy does not gate at all. So #586's "up means any HTTP
response" is not a property of the box: it is a property of the proxy, and under
kamal-proxy it is false unless the CLI passes `--force`.

**The flip's failure is a second gate, and the loop must treat it as one.** The first
cut stopped the old container after a flip that had already errored, leaving the app
serving 502 from a proxy with no healthy target. #586 forbids a two-container end
state; this is the no-container end state, reached from the other side. Nothing in
the map says the flip can fail after the health gate passed. It can.

**A failed release leaks its image.** The image is transferred before the gate runs,
so a release that fails its gate — or its flip — leaves an image on the box that no
ledger entry names. Ledger-driven retention only ever prunes what it recorded, so
every failed deploy leaks one image forever. Retention has to reconcile against what
is actually on the box, not just walk the ledger.

**Retention cannot be driven by `docker images`.** The first cut trusted its ordering
and pruned both the newest image and the one backing the running container, keeping a
release that had failed its gate. Deploy order has to be recorded — here as a file at
`/var/lib/ocel/releases`, which is also #589's "desired state as files on the box".

**The probe needs a network path that does not exist.** #586 has the CLI probe the
container over SSH, but a container on the shared network with no published port is
unreachable from the SSH session. This prototype probes from a throwaway container on
that network using the app image's own `node`, which only works because the app image
happens to carry one. The real options are publishing to `127.0.0.1` or shipping a
probe; neither is in the map.

**A hung app produces no logs.** #586 surfaces the failed container's logs in the
deploy output. An app that never reaches `listen` has written nothing, so the operator
gets an empty log and no reason. The failure output owes more than `docker logs`.

## What did not surprise

Direct `docker load` over SSH as `ocel-deploy` works on the bootstrap guarantee alone
— engine present, deploy user in the `docker` group, nothing else. Digest dedup skips
the transfer. `--restart unless-stopped` needs nothing around it. Rollback by
re-running a retained digest needs no new verb and no re-transfer. Both proxies flipped
0 of ~900 requests failed under continuous load.
