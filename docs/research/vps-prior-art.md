# Prior-art survey: custom domains, TLS, previews, env vars, DNS on a single Docker VPS

Scope: Kamal 2 (basecamp/kamal + kamal-proxy), Coolify, Dokploy, Dokku, briefly CapRover and Piku.
Read against ocel's framing: a VPS is a deploy *target*, not a platform home — no resident
control panel, agentless (desired state as files on the box), one resident reverse-proxy
container, containers labeled as the join key, deploys drive everything over SSH, state in a
generic record store on the box, blue-green cutover via proxy flip already decided.

---

## 1. Custom domains + TLS on the box

### Kamal 2 / kamal-proxy

- **Proxy**: kamal-proxy is Kamal 2's own Go reverse proxy (replaced Traefik in v2). One
  instance runs per server; the first app deployed installs it, later apps reuse it. Host
  routing is by `host`/`hosts` in `deploy.yml`; "if no hosts are set, then all requests will
  be forwarded, except for matching requests for other apps deployed on that server that do
  have a host set" — i.e. an explicit fallback/default-app slot exists.
  Source: https://kamal-deploy.org/docs/configuration/proxy/
- **TLS**: three modes exist in kamal-proxy:
  1. Automatic ACME via Let's Encrypt (`ssl: true` / `--tls`), HTTP-01-style, single server,
     port 443 required for challenge.
  2. Manual/custom certs: `--tls-certificate-path` / `--tls-private-key-path`, or in Kamal's
     YAML a `ssl: {certificate_pem, private_key_pem}` secret. Supports "certs issued by a
     different CA" (e.g. Cloudflare Origin Certificates), though one issue reports friction
     doing this (basecamp/kamal#1377).
     Source: https://github.com/basecamp/kamal-proxy
  3. **On-demand TLS**: `--tls-on-demand-url` — kamal-proxy calls an HTTP approval endpoint
     with the requested hostname before issuing a cert dynamically. Built for "serving
     customer domains" where the full host set isn't known at deploy time.
     Source: https://github.com/basecamp/kamal-proxy
- **No wildcard from kamal-proxy itself.** Maintainers/community are explicit: "the only way
  to get a wildcard certificate from Let's Encrypt is through DNS verification, which cannot
  be handled by Kamal/Kamal Proxy" — kamal-proxy has no DNS-01 support, so if you want a
  wildcard you must obtain it out-of-band (e.g. certbot + Route53 DNS plugin) and hand
  kamal-proxy the cert/key as a custom cert.
  Source: https://github.com/basecamp/kamal/discussions/1100
- **Cert storage**: `/home/kamal-proxy/.config/kamal-proxy/certs`, mode `0700`, using Go's
  `autocert` package; the cache path is scoped by a SHA-256 hash of the ACME directory URL so
  staging/prod caches don't collide. A `kamal-proxy.state` file (mode `0644`) records full
  service topology, container IDs, and the `acme_cache_path` pointing at exactly where certs
  live — flagged in a third-party security audit as a stronger-than-necessary disclosure
  surface, plus a risk that an attacker with container/PID-namespace access could read the
  domain private key and the ACME **account key** (letting them issue/revoke certs for any
  other domain on the same LE account).
  Source: https://bernardoamc.com/kamal-proxy-security-audit/
- **Multiple hosts/certs on one instance**: yes — each deployed service declares its own
  `host`/`hosts`, and "only one service at a time can route a specific host." For path-based
  routing under one host, "TLS options must be set on the root path" and other paths on that
  host inherit the same TLS settings. Mixing an ACME-issued cert and a manually supplied
  wildcard for *the same app* is not natively supported today; the workaround discussed is
  running a second, separately-configured `kamal-proxy deploy` targeting the same backend
  container, or waiting on in-flight PRs (#969, #1531) for multi-domain/multi-cert support.
  Source: https://github.com/basecamp/kamal/discussions/1100 ,
  https://kamal-deploy.org/docs/configuration/proxy/
- **Known failure modes**: "no viable challenge type found" when the domain can't be
  validated by any ACME challenge kamal-proxy offers (e.g. dynamic DNS domains, or hosts
  behind CDNs) — basecamp/kamal-proxy#26. "host must be set when using TLS" — TLS mode
  requires an explicit host, basecamp/kamal#1037.

### Coolify

- **Proxy**: Traefik by default (Caddy is an alternative proxy option in Coolify). Entering a
  domain with `https://` triggers Traefik's ACME HTTP-01 challenge automatically — "creates a
  temporary file on the server, Let's Encrypt fetches it over port 80... then issues the
  certificate." Renewal is automatic (LE certs are 90 days).
  Source: https://coolify.io/docs/knowledge-base/domains ,
  https://azdigi.com/en/blog/self-hosted/domain-dns-and-ssl-on-coolify-configure-domain-names-and-automatic-https
- **Wildcard**: requires switching Traefik from HTTP-01 to **DNS-01** — "wildcard
  certificates cannot be issued via the HTTP challenge." Setup needs a DNS-provider API
  token (e.g. Cloudflare token scoped `Zone:DNS:Edit` + `Zone:Zone:Read`) wired into Traefik,
  plus explicit router labels declaring the SAN set
  (`tls.domains[0].main` / `tls.domains[0].sans=*.domain`). Once wildcard is issued, "new
  deployments become reachable over HTTPS immediately instead of waiting for ACME issuance"
  — i.e. many-subdomains-per-cert amortizes issuance latency and rate-limit pressure.
  Source: https://coolify.io/docs/knowledge-base/proxy/traefik/wildcard-certs
- **Multiple apps/domains on one box**: normal case — each app/service gets its own Traefik
  router via labels Coolify generates; no per-app proxy process.
- **Failure modes (documented + issue-sourced)**: port 80/443 not reachable from the
  internet; Cloudflare (or any) proxying interfering with HTTP-01/TLS-ALPN-01 (fix: switch to
  DNS-01 or disable the proxy); missing/wrong A or AAAA records (if AAAA exists but IPv6 port
  80 is closed, the challenge still fails even though IPv4 is fine); LE rate-limiting
  (HTTP 429 in proxy logs, common on shared/reused IPs); WAF blocking LE's validation
  requests (403); stale `acme.json` requiring manual deletion (`rm
  /data/coolify/proxy/acme.json`) to force reissuance.
  Source: https://coolify.io/docs/troubleshoot/dns-and-domains/lets-encrypt-not-working
- **Real bug**: preview deployments generate invalid Traefik router rules when the main app
  has multiple comma-separated domains configured — coollabsio/coolify#7915. Evidence that
  label/router generation for N-domains-per-app is a real correctness hazard, not just an
  edge case.
  Source: https://github.com/coollabsio/coolify/issues/7915

### Dokploy

- **Proxy**: Traefik, standalone container by default or a Swarm service in multi-node mode.
  Dokploy injects Traefik labels into the generated compose file automatically — "you don't
  need to add them manually." Dynamic file-provider configs land under
  `/etc/dokploy/traefik/dynamic/{name}.yml` and are hot-reloaded on change.
  Source: https://deepwiki.com/Dokploy/dokploy/8.2-traefik-configuration
- **Zero-config domains**: Dokploy defaults every app to a free `*.traefik.me` hostname
  (wildcard DNS→any IP baked into the `traefik.me` name itself, e.g.
  `{app}.{ip}.traefik.me` resolves to `{ip}`) with **no TLS** — "traefik.me domains are free,
  but they are limited to HTTP only." HTTPS on a free domain requires bringing your own cert.
  Source: WebSearch summary of https://docs.dokploy.com/docs/core/domains ,
  https://docs.dokploy.com/docs/core/applications/preview-deployments
- **Custom domain + TLS**: user creates an A/AAAA record (or wildcard `*` record for preview
  support) themselves; Traefik then does the standard ACME HTTP-01 flow, requiring ports
  80/443.
- **Known gap**: Compose-based services don't reliably get their Traefik dynamic-config file
  generated when domains are added (application services do, compose services sometimes
  don't) — Dokploy/dokploy#2994. Also requires the container to be attached to the
  `dokploy-network` *and* carry `traefik.docker.network`, or routing silently fails.
  Source: https://github.com/Dokploy/dokploy/issues/2994

### Dokku

- **Proxy**: nginx (default) is generated/templated per-app by Dokku; Traefik is a
  documented alternative but wildcard/DNS-01 support for the Traefik path is an open
  request (dokku/dokku#6423), not shipped.
- **TLS**: the `dokku-letsencrypt` plugin wraps `acme.sh`/lego-style clients, HTTP-01 by
  default, one cert per app/domain set. **DNS-01 (and therefore wildcards) landed later** and
  is opt-in: `dokku letsencrypt:set --global dns-provider <name>` plus
  `dokku letsencrypt:set --global dns-provider-<PROVIDER_ENV_VAR> <value>` per lego provider
  (Cloudflare, Route53, Namecheap, ...). The maintainers were explicit for years that
  "wildcard support is not officially supported by this plugin" absent sponsorship/volunteer
  work (dokku-letsencrypt#148, #189) before eventually shipping provider-based DNS-01.
  Source: https://github.com/dokku/dokku-letsencrypt ,
  https://github.com/dokku/dokku-letsencrypt/issues/189
- **Apex vs subdomain / multi-app routing footgun**: subdomains are inferred from the app
  name plus a global domain (`dokku domains:add-global`); "by default, Dokku will route any
  received request with an unknown HOST header value to the lexicographically first site in
  the nginx config stack" — i.e. misconfigured/absent Host routing silently falls through to
  whichever app sorts first alphabetically, a known source of "wrong app served at my domain"
  reports (dokku/dokku#1646, #2608).
  Source: https://dokku.com/docs/configuration/domains/ ,
  https://github.com/dokku/dokku/issues/1646
- **Prerequisite discipline**: docs explicitly warn "before you run `letsencrypt:enable`,
  make sure every domain you plan to cover... actually resolves to your server" — i.e. Dokku
  does not itself verify DNS before attempting issuance; a bad/unpropagated record just fails
  the ACME challenge.

### CapRover (brief)

- **Proxy**: nginx, config generated/managed by CapRover's captain daemon.
- **TLS**: one-click "Enable HTTPS" runs `certbot certonly --webroot` (HTTP-01) per domain.
  Wildcard is possible only by hand-editing `certbotCertCommandRules` in
  `/captain/data/config-override.json` to swap in a DNS-01 plugin (worked Cloudflare example
  given in docs) and requesting `-d domain -d "*.domain"` as **two** SAN entries — this
  requires a custom certbot Docker image bundling the DNS plugin, since the stock image lacks
  third-party plugins. Not point-and-click.
  Source: https://caprover.com/docs/certbot-config.html
- Setup asks for a wildcard DNS entry up front (`*.yourdomain` → server IP) purely so
  CapRover's own subdomain-per-app scheme works over HTTP before certs exist.
  Source: https://caprover.com/docs/get-started.html

### Piku (brief)

- Minimal PaaS: nginx + uWSGI, "will also set up either a private certificate or obtain one
  via Let's Encrypt to enable SSL," `NGINX_HTTPS_ONLY` forces the HTTPS redirect. No
  DNS-01/wildcard story documented — closest in spirit to kamal-proxy's original HTTP-01-only
  scope, but with none of kamal-proxy's on-demand-approval or custom-cert plumbing.
  Source: https://github.com/piku/piku , https://piku.github.io/configuration/env.html

### Targeted question — Caddy: config-file-driven resident proxy with zero-downtime swaps?

Yes, and this is Caddy's headline operational property. The Admin API's `/load` endpoint
"sets Caddy's configuration, overriding any previous configuration" and is documented as
graceful and zero-downtime: **the new config is started before the old one is stopped**, so
for a brief overlap window both configs run concurrently; in-flight requests on the old
config are drained rather than dropped, and if the new config fails validation it's rolled
back with the old config simply never having stopped. `caddy reload` (CLI) does the same
thing against a running instance by adapting the file to JSON and POSTing it to the local
admin socket. This maps directly onto ocel's "config-file-driven resident proxy" plan: point
one adapter at a directory of app configs, `POST` to `/load` (or `/config/...` for partial
patches) on every deploy, and cutover is atomic from the proxy's point of view — no separate
"drain then swap" choreography needed in the deploy script itself.
Source: https://caddyserver.com/docs/api , https://caddyserver.com/docs/command-line

### Targeted question — kamal-proxy: multi-host distinct certs / storage / on-demand limits / wildcard?

- **Multi-host, distinct certs**: yes, per-service `host`/`hosts` each get routed and (if
  `ssl`/`tls` is set) certified independently; the constraint is "only one service at a time
  can route a specific host," not one cert per proxy instance.
- **Cert storage**: filesystem, `~/.config/kamal-proxy/certs`, `0700`, keyed via Go
  `autocert`, cache path namespaced by a hash of the ACME directory (dev/staging/prod don't
  collide). A `kamal-proxy.state` file separately records the `acme_cache_path` plus full
  service topology at `0644` — broader read exposure than the certs themselves.
  Source: https://bernardoamc.com/kamal-proxy-security-audit/
- **On-demand TLS**: supported via `--tls-on-demand-url`, an HTTP approval callback invoked
  with the incoming hostname before kamal-proxy will attempt issuance for it — designed for
  "serving customer domains" (multi-tenant SaaS) where hosts aren't known at deploy time.
  No documented rate/quantity limit beyond what Let's Encrypt itself imposes.
  Source: https://github.com/basecamp/kamal-proxy
- **Wildcard**: not possible through kamal-proxy itself — no DNS-01 client. Only path is
  obtaining a wildcard cert externally and loading it as a **manual/custom** cert via
  `--tls-certificate-path`/`--tls-private-key-path` (or Kamal's `ssl: {certificate_pem,
  private_key_pem}` secret). Confirmed by both the README's absence of any DNS-provider
  config surface and by maintainer/community discussion.
  Source: https://github.com/basecamp/kamal/discussions/1100

---

## 2. Preview environments on a VPS

### Kamal 2

No native PR/preview-deploy feature. Community write-ups show running full separate
`deploy.yml` configs (distinct `service` name, distinct `host`) per environment/branch, which
means N complete extra kamal-proxy-routed apps rather than an ephemeral, auto-cleaned preview
system. There's no wildcard-subdomain-per-PR primitive and no TTL/cleanup automation in Kamal
itself.
Source: https://www.honeybadger.io/blog/new-in-kamal-2/ ,
https://nts.strzibny.name/multiple-apps-single-server-kamal-2/

### Coolify — has first-class preview deploys

- **URL formation**: templated — `{{pr_id}}`, `{{random}}`, `{{domain}}` tokens; default
  template `{{pr_id}}.{{domain}}` (e.g. PR 123 → `123.preview.example.com`).
- **DNS**: user must pre-create a wildcard `A`/`AAAA` record for the preview zone
  (`*.preview.example.com` → server IP) — Coolify does not create this record itself.
- **TLS for the wildcard preview zone**: not documented as automatic; in practice this only
  works cleanly once the Traefik **DNS-01 wildcard** setup (section 1) is already in place, so
  every new PR subdomain is instantly covered by the existing wildcard cert rather than
  triggering a fresh per-PR HTTP-01 issuance (which would also hit the "issue on every PR"
  rate-limit risk).
- **Lifecycle**: deleted automatically when the PR/MR is closed or merged; no time-based TTL
  — cleanup is event-driven off the source-control webhook, not a cron/expiry.
- **Env vars**: previews get their **own separate variable group**, distinct from production
  — explicit recommendation to use non-prod credentials since preview containers, though
  ephemeral, may still touch external state.
Source: https://next.coolify.io/docs/applications/deployments/preview-deployments

### Dokploy — also first-class preview deploys

- **URL formation**: pattern `<branch>-<pr-number>.<wildcard-domain>`; if no custom domain is
  configured, defaults to the free `traefik.me` wildcard service (`preview-{app}-{id}.
  {ip}.traefik.me`), HTTP only.
- **DNS**: for a real custom domain, same requirement as Coolify — user points a wildcard `*`
  A record at the server; Dokploy does not create it.
- **TLS**: previews "inherit the same [security/redirect] configuration" as the parent app,
  so if the parent has TLS/HTTPS-redirect configured, previews get it too (implying the same
  wildcard-cert prerequisite as Coolify for anything beyond `traefik.me`).
- **Lifecycle**: triggered by PR open, redeployed on each new commit, "clean up when the pull
  request is closed or merged." Default cap of **3 concurrent preview deployments per app**
  (configurable) — an explicit resource-bound absent from Coolify's docs.
- **Env vars**: a magic `${{DOKPLOY_DEPLOY_URL}}` variable is injected so app config can
  reference its own generated preview URL.
Source: https://docs.dokploy.com/docs/core/applications/preview-deployments

### Dokku / CapRover / Piku

None ship PR-preview automation. Dokku's closest primitive is scriptable multi-app
deployment (`dokku apps:create pr-123-myapp`) driven externally by CI, with the same
"lexicographically-first nginx site" host-routing footgun from section 1 if the Host header
isn't set correctly. CapRover explicitly scoped this out (issue #684: "the project wants to
keep the scope small" — multiple apps are possible but "not connected in any way
semantically or architecturally"). Piku has no preview concept at all.
Source: https://github.com/caprover/caprover/issues/684

### What transfers to ocel

- The **traefik.me pattern** (a third-party-hosted wildcard-DNS-to-IP service, HTTP-only) is
  the cleanest zero-config bootstrap for "preview URL exists before you've touched real DNS,"
  directly analogous to sslip.io/nip.io (below) — but it only solves naming, never TLS.
- Both Coolify and Dokploy converge on the same shape: **wildcard subdomain + wildcard cert
  is a prerequisite investment, previews are "free" only after that's paid once.** For
  ocel's agentless model, this argues for treating "wildcard cert provisioning" as a one-time,
  explicit, DNS-01-driven bootstrap step per box/domain (needs a DNS provider API token from
  the user), after which preview cutover is pure proxy-config-file churn — no per-preview
  ACME round-trip, no LE rate-limit exposure per PR.
- Neither tool automates the wildcard DNS record itself — that's consistently left to the
  human. DNS-01 API credentials are used **only** for the ACME TXT challenge, never to
  provision the app-facing A/AAAA/wildcard record (see section 4).
- TTL-less, webhook-driven cleanup (delete-on-PR-close) is the norm; nobody polls or expires
  by wall-clock. Fits an agentless model poorly only in that the *trigger* must come from
  somewhere (CI/webhook) — ocel's CLI-driven-over-SSH model would need the close event
  delivered from outside the box, since there's no resident agent watching PR state.

---

## 3. Env vars / secrets into containers

### Kamal 2

- Explicit split in `deploy.yml`: `env.clear` (inline plaintext values, passed straight
  through to `docker run` as `-e`) vs `env.secret` (a bare name list; the *value* is resolved
  from `.kamal/secrets` — a dotenv-style file **read on the deploying machine**, not
  necessarily present on the box). Kamal renamed the load path from `.env` to `.kamal/secrets`
  in v2 specifically to avoid clashing with other tools' `.env` conventions.
  Source: https://kamal-deploy.org/docs/configuration/environment-variables/
- On the box, secret values end up in **an env file on the host** rather than inline in the
  `docker run` invocation ("secrets are not passed directly to the container but are stored
  in an env file on the host") — this avoids the value leaking into shell history / `ps`
  output / the Docker daemon's command log, but does **not** avoid `docker inspect` exposure:
  whether a var reaches the container via `-e` or via `--env-file`, Docker folds it into the
  container's `Config.Env`, which `docker inspect` prints either way. Docker's own docs and
  general consensus: file-vs-flag changes *process-list* exposure, not *inspect* exposure —
  only bind-mounted secret files (Compose's `/run/secrets/*` pattern, or Docker Swarm
  secrets) actually keep a value out of `docker inspect`.
- The kamal-proxy security audit separately flags that **application** env vars (DB
  credentials, API keys) are trivially readable to anyone with exec/RCE access to the
  container regardless of delivery mechanism, and that kamal-proxy's own structured
  request-logging middleware can leak secrets carried in query strings (OAuth callbacks,
  pre-signed URLs, webhook tokens) into the proxy's logs — a delivery-adjacent leak vector
  worth flagging for ocel's own proxy logging design.
  Source: https://bernardoamc.com/kamal-proxy-security-audit/

### Coolify

Writes a `.env` file after build containing all runtime-enabled variables, loaded into the
container via Docker Compose's `env_file` directive at start — i.e. **baked to a file on the
box**, not passed as literal `-e` flags. Coolify layers extra semantics on top: multiline
values get single-quoted to block shell interpolation, and a "Literal" checkbox opts a value
out of Coolify's own `$OTHER_VAR`-reference expansion. No documented secret-rotation
primitive beyond editing the value and redeploying (which regenerates the `.env` and restarts
the container) — rotation is "redeploy," not a live push.
Source: WebSearch summary of https://coolify.io/docs/knowledge-base/environment-variables

### Dokku

- `config:set` writes to Dokku's own config store per app; for buildpack deploys this is
  materialized to `/app/.env` at deploy time (**not** live-updated by `config:set` alone —
  only refreshed on the next `deploy`/`ps:rebuild`, a documented staleness gotcha).
- **Deliberate build/runtime separation**: "for security reasons — and as per docker
  recommendations — Dockerfile-based deploys have variables available only during runtime,"
  while buildpack deploys expose vars at both build and run time. This is Dokku explicitly
  reducing one leak surface (vars baked into image layers/build logs) for the Dockerfile path
  that Kamal/Coolify/Dokploy (all Dockerfile/Compose-first) don't distinguish as cleanly.
- No built-in secret manager; values are "in plain-text on disk," docs say access control to
  the Dokku host itself is the security boundary, not the config store.
Source: https://dokku.com/docs/configuration/environment-variables/

### Piku

Simplest of the surveyed tools: an `ENV` file committed to (or placed alongside) the app
repo, parsed and exported before the app's Procfile-driven process starts. No secret/clear
distinction, no separate store — whatever's in `ENV` is plaintext on the box.
Source: https://piku.github.io/configuration/env.html

### What transfers to ocel

- The kamal split (declare-secret-by-name in versioned config, resolve-value from an
  out-of-band file) is the right shape for "desired state as files on the box" *if* the
  resolved values also land in an out-of-band file on the box rather than inline in the
  `docker run`/compose invocation — matches ocel's agentless model (SSH pushes a rendered
  env-file, container picks it up via `--env-file`/`env_file:`).
- **Important correction for ocel's design**: don't market env-file delivery as hiding
  secrets from `docker inspect` — it doesn't. If "not visible via `docker inspect`" is an
  actual trust requirement, the only mechanism that delivers it is a **file mount** into the
  container (Compose/Swarm secrets pattern, `/run/secrets/<name>`), not an env file consumed
  as environment variables. Every surveyed tool (Kamal, Coolify, Dokku, Dokploy, Piku) treats
  "env vars" and "inspectable" as coupled facts of life and none claims otherwise in their
  own docs.
- Dokku's build/run separation for Dockerfile deploys is worth replicating: ocel should never
  pass secret-valued build args to `docker build` for a Dockerfile-based app, only at
  `docker run`/compose time.

---

## 4. DNS automation

**Consistent finding across every tool surveyed: none of them write the user's app-facing
DNS record (A/AAAA/CNAME) on their behalf.** All five docs sets instruct the user to create
the record manually and simply wait/verify. The only place any of these tools calls out to a
DNS provider's API automatically is the **ACME DNS-01 challenge TXT record**, and only when
DNS-01 is explicitly configured (Coolify's Traefik wildcard setup, Dokku's
`dns-provider`/lego integration, CapRover's custom `certbotCertCommandRules`). That's a
narrow, ACME-scoped write (create/delete one `_acme-challenge` TXT record), not general DNS
record management, and it requires the user to supply their own scoped API token
(Cloudflare example: `Zone:DNS:Edit` + `Zone:Zone:Read`).
Source: https://github.com/coollabsio/coolify/discussions/2871 (feature request, unimplemented),
https://coolify.io/docs/knowledge-base/proxy/traefik/wildcard-certs ,
https://github.com/dokku/dokku-letsencrypt

**DNS verification before issuing certs**: also universally absent as an explicit pre-check.
Dokku's docs put the burden on the operator ("make sure every domain... actually resolves to
your server" before running `letsencrypt:enable"); Coolify's troubleshooting doc describes
diagnosing failures *after the fact* via `dig`, not a built-in pre-flight check. No surveyed
tool does a DNS pre-check before firing the ACME request — validation failure is discovered
via the ACME response itself (timeout / "no viable challenge type" / 403 from a WAF).

### The sslip.io / nip.io / traefik.me trick

- **sslip.io / nip.io**: wildcard-DNS services that resolve `<anything>-<ip-encoded>.sslip.io`
  (or `nip.io`) to the embedded IP without any provisioning step — e.g.
  `192-168-1-100.sslip.io` → `192.168.1.100`. (sslip.io now redirects to nip.io, same
  mechanism/operator lineage.) This is a **pure DNS trick**: it does not issue or serve TLS
  certs itself. It exists purely so that ACME's HTTP-01 challenge (which needs *some*
  resolvable hostname pointing at the box) can succeed without the user owning/configuring a
  real domain — Let's Encrypt still issues a normal per-host cert via HTTP-01 against the
  `sslip.io`/`nip.io` name.
  Source: https://nip.io/ (redirect target of https://sslip.io)
- **traefik.me** (used by Dokploy for its zero-config default domain): same wildcard-to-IP
  DNS trick, but Dokploy's docs are explicit that this path is **HTTP-only** — "limited to
  HTTP only" — i.e. Dokploy did not wire traefik.me into automatic TLS issuance the way
  sslip.io/nip.io can be (in principle) combined with HTTP-01. Getting HTTPS on a
  `traefik.me` name requires bringing your own cert.
  Source: WebSearch summary of https://docs.dokploy.com/docs/core/domains

### What transfers to ocel

- A sslip.io/nip.io-style zero-DNS bootstrap domain is a strictly-better default than
  Dokploy's `traefik.me` choice if ocel wants day-one HTTPS with zero user DNS action:
  since sslip.io/nip.io names are ordinary resolvable hostnames, they're HTTP-01-eligible out
  of the box — Dokploy's decision to leave `traefik.me` at HTTP-only looks like a product
  gap, not a technical ceiling.
- Don't build DNS-record automation as an MVP feature — no competitor has shipped it despite
  at least one explicit user request (Coolify discussion #2871 sat open). The DNS-01
  TXT-record automation Coolify/Dokku *do* have is narrowly scoped to the ACME handshake and
  reuses the DNS provider's existing lego/acme.sh integration, not a bespoke DNS-management
  layer — that's the cheaper, already-proven slice to build if/when ocel adds DNS-01/wildcard
  support.
- Every tool's failure-mode story (port 80 closed, DNS not propagated, CDN/WAF interference,
  rate limits) is discovered reactively via the ACME response, never pre-checked. Given
  ocel's Trust > Ops priority, doing a cheap pre-flight (resolve the hostname from the box
  itself, or from a public resolver, before firing the ACME request; surface "DNS doesn't
  point here yet" as a distinct, actionable CLI error rather than surfacing raw ACME failure
  text) would already be differentiated versus every tool surveyed here.

---

## Let's Encrypt operational constraints (reference)

- **50 certificates per registered domain per 7 days** (refill: 1 per ~202 minutes) — the
  binding constraint for a multi-tenant box issuing per-subdomain certs under one apex domain
  (e.g. many preview/customer subdomains under `*.ocel-boxes.example.com`).
- **300 new orders per account per 3 hours** — LE's own guidance for multi-tenant operators is
  to *reuse one account* across customers rather than mint per-customer accounts.
- **5 certificates per exact identifier set per 7 days** — guards against redeploy-loop
  reissuance of the literal same host set; relevant if ocel ever re-requests a cert on every
  deploy instead of caching/reusing.
- **5 failed authorizations per identifier per hour per account** — the practical ceiling on
  "retry loop while debugging a broken DNS/port-80 setup"; argues for the pre-flight check
  above, and for testing against LE's staging environment during ocel's own integration
  tests.
- **DNS-01 is the only path to a wildcard cert**, full stop, across every ACME CA including
  Let's Encrypt — confirmed identically by Coolify, Dokku, kamal-proxy, and CapRover's own
  docs/discussions. There is no HTTP-01 wildcard path; anything claiming otherwise in a
  tool's marketing is wrong.
Source: https://letsencrypt.org/docs/rate-limits/

---

## Sources (all, by topic)

**Kamal / kamal-proxy**
- https://github.com/basecamp/kamal-proxy
- https://kamal-deploy.org/docs/configuration/proxy/
- https://kamal-deploy.org/docs/configuration/environment-variables/
- https://github.com/basecamp/kamal-proxy/issues/26
- https://github.com/basecamp/kamal/issues/1037
- https://github.com/basecamp/kamal/issues/1377
- https://github.com/basecamp/kamal/discussions/1049
- https://github.com/basecamp/kamal/discussions/1100
- https://github.com/basecamp/kamal/discussions/964
- https://github.com/basecamp/kamal-proxy/discussions/142
- https://bernardoamc.com/kamal-proxy-security-audit/
- https://www.honeybadger.io/blog/new-in-kamal-2/
- https://nts.strzibny.name/multiple-apps-single-server-kamal-2/
- https://nts.strzibny.name/kamal-proxy/

**Coolify**
- https://next.coolify.io/docs/applications/deployments/preview-deployments
- https://coolify.io/docs/knowledge-base/proxy/traefik/wildcard-certs
- https://coolify.io/docs/knowledge-base/domains
- https://coolify.io/docs/knowledge-base/environment-variables
- https://coolify.io/docs/troubleshoot/dns-and-domains/lets-encrypt-not-working
- https://github.com/coollabsio/coolify/issues/7915
- https://github.com/coollabsio/coolify/discussions/2871
- https://github.com/coollabsio/coolify/discussions/8987
- https://azdigi.com/en/blog/self-hosted/domain-dns-and-ssl-on-coolify-configure-domain-names-and-automatic-https

**Dokploy**
- https://docs.dokploy.com/docs/core/applications/preview-deployments
- https://docs.dokploy.com/docs/core/domains
- https://github.com/Dokploy/dokploy/issues/2994
- https://github.com/Dokploy/dokploy/discussions/2051
- https://github.com/Dokploy/dokploy/discussions/2057
- https://deepwiki.com/Dokploy/dokploy/8.2-traefik-configuration

**Dokku**
- https://github.com/dokku/dokku-letsencrypt
- https://github.com/dokku/dokku-letsencrypt/issues/189
- https://github.com/dokku/dokku-letsencrypt/issues/148
- https://github.com/dokku/dokku/issues/6423
- https://dokku.com/docs/configuration/domains/
- https://dokku.com/docs/configuration/environment-variables/
- https://github.com/dokku/dokku/issues/1646
- https://github.com/dokku/dokku/issues/2608

**CapRover**
- https://caprover.com/docs/certbot-config.html
- https://caprover.com/docs/get-started.html
- https://github.com/caprover/caprover/issues/387
- https://github.com/caprover/caprover/issues/1761
- https://github.com/caprover/caprover/issues/684

**Piku**
- https://github.com/piku/piku
- https://piku.github.io/configuration/env.html
- https://piku.github.io/features.html

**Caddy**
- https://caddyserver.com/docs/api
- https://caddyserver.com/docs/command-line

**Let's Encrypt / DNS tricks / secrets**
- https://letsencrypt.org/docs/rate-limits/
- https://nip.io/ (redirect target of https://sslip.io)
- https://www.wiz.io/academy/container-security/docker-secrets
- https://www.sourcery.ai/vulnerabilities/container-secrets-environment-variables
