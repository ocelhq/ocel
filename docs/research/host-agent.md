# Host-side agent: what would ever need one

Sources pinned to the commits read: kamal `eee0083b38661c3707c6b6052cc89e85038a096c`,
kamal-proxy `fb0f36c386b4bbcb561c936cc10baf2ae497a656`, coolify
`44e4f071b1e3340c516cab2cc6d8c4738d47087d`, dokku
`c554cd40adb7de828dee528349bca4df5190e685`, dokku-event-listener
`a897b9597cca57d23c3e7b6b14e7042a0e77445a`, dokku-letsencrypt
`bd8987d8a5ef4c019ba5c68ee2395e35b631abe5`, caprover
`c352c9785464caf75166274867f00bc054fa6ce2`.

Companion to `docs/research/host-bootstrap-prior-art.md`, which covers what these systems
create on a fresh box. This one asks what has to keep running on it.

## Short answer

- The question is not agentless versus agent. #586's cutover already buys one resident
  process on the box, the reverse proxy. Everything the ticket lists as agent work splits
  three ways: things that proxy already does, things a cron line does, and a short list
  that genuinely needs a second resident process.
- Recommendation for #586's Q3, who probes at deploy time: the proxy probes, the CLI blocks
  on the exit code. That is what kamal does, and it wins on correctness rather than
  convenience. The proxy probes over the network path traffic will take, and gate-and-flip
  becomes one operation instead of two round trips with a gap between them.
- The most surprising fact in the prior art. kamal-proxy deliberately stops health-checking
  a single-target service once it goes healthy. Between deploys nothing on a kamal box is
  probing anything. A wedged but running container serves 502s until a human notices.
- Of the four systems surveyed, only kamal is genuinely agentless, and it is also the only
  one with no console. Dokku runs `dokku-event-listener` as a systemd service and three
  scheduled jobs on the box. CapRover runs docker swarm, which means dockerd is the agent.
  Coolify shipped Sentinel. Agentless is not the industry default; it is kamal's position,
  and kamal has nobody to report to.
- Coolify already ran this experiment, and its trajectory is the finding. Agentless SSH,
  then a reporter agent (Sentinel) that arrived for metrics, then accepted design documents
  for an actuator agent (`coold`) once the control plane rather than a human became the
  operator. No step of that was forced by deploys.
- Named fog that would force an agent, all of it continuous and outbound: push log
  shipping, metrics, crash-loop alerting with nobody deploying, deploys with no operator
  SSH session. Section 5 lists them.
- Going agentless now does not foreclose an agent, as long as three properties hold in the
  cutover contract (section 4). Desired state lives in files on the box, the flip is one
  idempotent addressable call against the proxy, and container labels are the join key so
  any observer can rebuild state from `docker ps`.

## 1. What the prior art actually does

### 1.1 kamal: the proxy is the prober, and then it stops probing

`kamal deploy` never probes from the operator's machine. The health gate is a command run
inside the proxy container over SSH.

```ruby
def deploy(target:)
  proxy_exec :deploy, role.container_prefix, *role.proxy.deploy_command_args(target: target)
end

def proxy_exec(*command)
  docker :exec, proxy_container_name, "kamal-proxy", *command
end
```

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/app/proxy.rb

The arguments it passes are the whole gate policy: `deploy-timeout`, `drain-timeout`,
`health-check-interval`, `health-check-timeout`, `health-check-path`.

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/proxy.rb

kamal-proxy's README states the contract the CLI relies on.

> It will immediately begin running HTTP health checks to ensure it's reachable and
> working and, as soon as those health checks succeed, will start routing traffic to it.
> If the instance fails to become healthy within a reasonable time, the `deploy` command
> will stop the deployment and return a non-zero exit code, allowing deployment scripts to
> handle the failure appropriately.

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/README.md

So the CLI's entire participation in the health gate is an exit code. The probe lives on
the box, inside a process that is resident for an unrelated reason.

Then it stops. From `LoadBalancer.updateHealthyTargets`:

```go
// If we have a single target, we can stop health-checking once it's
// healthy. Even if it becomes unhealthy later, taking it out of the pool
// won't help.
if !lb.multiTarget && len(lb.writers) == 1 {
    lb.all.StopHealthChecks()
}
```

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/internal/server/load_balancer.go

`StopHealthChecks` cancels the ticker goroutine outright (`hc.cancel()` in
`internal/server/health_check.go`). The reasoning is honest, and it describes Ocel's
situation exactly. With one target there is nowhere to fail over to, so knowing the target
is sick buys nothing at the proxy. It buys plenty at the console, but kamal has no console.

With more than one target the checker keeps ticking and unhealthy targets drop out of
`lb.writers`. Continuous probing on a single-VPS box is therefore a proxy configuration
question rather than an agent question. You are asking the resident proxy to keep doing
what it already knows how to do, and to tell someone.

### 1.2 kamal: supervision, GC and logs

Restart policy is `unless-stopped` for both app and proxy, and it is the only supervision
on the box. `"--restart", role.restart_policy`, where `restart_policy` falls back to
`"unless-stopped"`.

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/app.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/role.rb

Image and container GC happens at deploy time, not on a schedule. `kamal deploy` invokes
`kamal:cli:prune:all` as its last step, and there is no cron anywhere in kamal. Retention
is `retain_containers`, default 5, enforced by `tail -n +N` over a sorted `docker ps`
listing.

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/main.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/prune.rb

Logs are pull-only. `kamal app logs` is `docker logs` piped through `grep`, run over SSH,
and `--follow` holds the SSH session open for as long as the operator watches. Nothing is
shipped, nothing outlives the container, and `prune` deletes the containers whose logs you
would want after an incident.

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/app/logging.rb

Metrics exist and go nowhere. kamal-proxy links Prometheus and serves `/metrics` on its own
listener, but only when `--metrics-port` is non-zero, and the flag's default is `0`,
documented as "default zero to disable".

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/internal/cmd/run.go

An exposed endpoint is not shipping. Something has to either scrape it inbound, which means
a port opened toward the console plus an auth problem, or push it outbound, which means a
resident process.

### 1.3 kamal: boot-time reconciliation, and the lie it tells

The proxy writes its routing table to a JSON snapshot on every mutating operation
(`saveStateSnapshot` is deferred from deploy, remove, pause, resume and the rest) into the
`kamal-proxy-config` named volume, and reloads it at startup.

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/internal/server/router.go

On restore it does this:

```go
s.active = NewLoadBalancer(activeTargets, …)
s.active.MarkAllHealthy()
```

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/internal/server/service.go

After a host reboot, docker's `unless-stopped` policy brings the app and the proxy back,
and the proxy assumes the restored target is healthy without probing it. If the app comes
back broken, the proxy routes to it anyway. That is the real seam in the agentless boot
story. Reconciliation is "docker restarts what it was running" plus a proxy that trusts its
own last snapshot. Nothing re-derives truth.

### 1.4 coolify: the system that ran the experiment

Coolify is the most useful data point here, because it started where #586 is starting and
has since hit the wall twice.

It is agentless in the same sense Ocel would be. Every operation is a literal `ssh`
invocation with the remote command fed in as a heredoc. `SshMultiplexingHelper` assembles
`ssh -i <key> … user@host 'bash -se' <<…`, with `ssh -fN -o ControlMaster=auto -o
ControlPath=$mux -o ControlPersist=…` for connection reuse. The documented server
requirement is "SSH connectivity between Coolify and the server with SSH key
authentication".

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Helpers/SshMultiplexingHelper.php
https://coolify.io/docs/knowledge-base/server/introduction

Restart policy and health checks match kamal's shape. `const RESTART_MODE =
'unless-stopped'` applies to app containers, the proxy, tunnels and every database action.
A compose `healthcheck` is injected only when the image's Dockerfile declares none. The
deploy gate polls `docker inspect --format='{{json .State.Health.Status}}'` over SSH for
the duration of the deploy window and no longer, in
`ApplicationDeploymentJob::health_check()`.

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/bootstrap/helpers/constants.php
https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Jobs/ApplicationDeploymentJob.php

All the scheduling lives on the control plane rather than on the box. `app/Console/Kernel.php`
is Laravel's own scheduler: `ServerManagerJob` every minute, fanning out connection checks,
`ServerCheckJob`, `ServerStorageCheckJob` and a weekly patch check; `ScheduledJobManager`
every minute for user backups and tasks; `RegenerateSslCertJob` twice daily; a daily family
of `cleanup:*` commands. Coolify installs no cron on the managed server for any of it.

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Console/Kernel.php

Its answer to "continuous probing between deploys" is therefore to poll every box over SSH
once a minute from somewhere else. `ServerCheckJob` calls `GetContainersStatus`, which reads
`State.Status`, `State.Health.Status` and `RestartCount`. When `RestartCount` has grown it
records `last_restart_type => 'crash'`. When a per-application `max_restart_count` is
exceeded it dispatches `StopApplication` and sends the team a `RestartLimitReached`
notification. Below that limit it restarts nothing itself and lets docker's policy do the
work.

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Actions/Docker/GetContainersStatus.php

That is crash-loop alerting with no host agent, and it is worth studying for what it costs.
A control plane that must be permanently up, holding SSH credentials to every customer box,
polling every box every minute. The code carries the scars: exponential backoff for
unreachable servers out to roughly hourly, and a comment naming "avoid thundering herd" as
the reason server IDs are hashed into the check cycle.

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Jobs/ServerManagerJob.php

Then they wrote the reporter anyway. Sentinel is "an open-source lightweight container"
doing "Server resource monitoring (CPU, RAM usage for now)" and "Container resource
monitoring", started as `docker run -d --name coolify-sentinel -v
/var/run/docker.sock:/var/run/docker.sock -v $mountDir:/app/db --pid host …` with
`PUSH_ENDPOINT` and `PUSH_INTERVAL_SECONDS` in its environment. It pushes to a
token-authenticated `SentinelController::push()`, which updates a heartbeat and dispatches
`PushServerUpdateJob`, the same reconciliation the SSH poll performs, fed by push instead of
pull. When Sentinel is live the SSH poll is skipped: `ServerManagerJob` sends no
`ServerConnectionCheckJob` to a sentinel-live server and only dispatches `ServerCheckJob`
once the heartbeat has gone stale past `sentinel_push_interval_seconds * 3`, floor 120s.

https://coolify.io/docs/knowledge-base/server/sentinel
https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Actions/Server/StartSentinel.php
https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/app/Http/Controllers/Api/SentinelController.php
https://github.com/coollabsio/sentinel

Two things follow. The agent arrived for metrics, CPU and RAM, exactly the capability that
section 2 marks as having no agentless path, and only afterwards absorbed container state.
And the reporter is a drop-in replacement for the poller rather than a new subsystem, since
both feed the same job. That is the cleanest evidence available that agentless now does not
foreclose later, as long as the ingest is written against observations rather than against
the return value of an SSH session.

The actuator is next. The repo carries four accepted design documents for an unreleased v5
that replaces the SSH model with a per-host agent, `coold`, plus a connection broker.
"coold can solve the host inbound problem by dialing out, but Laravel request workers should
not own thousands of long-lived HTTP/2 agent streams", with "Hosts can remain behind NAT
because coold dials out to Flux" listed as a positive consequence, against a negative of
"More moving parts than a direct SSH/Docker model".

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/docs/v5/architecture/adr/0003-flux-connection-broker-boundary.md
https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/docs/v5/architecture/adr/0002-coold-host-agent-boundary.md

Those describe software that has not shipped, not current behaviour, and should be read that
way. The trajectory is the finding. Agentless, then a reporter agent for metrics, then an
actuator agent once the control plane is the operator. In their own words the forcing
function is not deploys. It is the number of connections and the hosts the control plane
cannot reach inbound.

### 1.5 dokku: already has an agent, and nobody calls it that

The assumption going in was that dokku is agentless. It is not, and the correction is the
single most useful thing in this document.

Dokku runs `dokku-event-listener` as a persistent systemd service on the box. The docs say
so plainly: "Dokku also runs `dokku-event-listener` in the background via the system's init
service." It is a separate repo, pinned in `contrib/dependencies.json`, and a `Recommends`
in `debian/control`.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/docs/processes/process-management.md

What it does is the crash-loop capability, and its whole scope is one event and one action.
It tails the docker event stream, and on a container `die` event calls
`shouldRebuildOnDie(restartPolicy, restartCount)`. If the policy is `on-failure` with a
positive `MaximumRetryCount` and `RestartCount` has reached it, the listener shells out to
`dokku --quiet ps:rebuild <app>`. The predicate reads
`restartPolicy.IsOnFailure() && restartPolicy.MaximumRetryCount > 0 && restartCount >=
restartPolicy.MaximumRetryCount`, and the comment above it explains the exclusion: a `die`
event only means "Docker has permanently given up restarting it" under `on-failure`, while
"Containers using `always`/`unless-stopped` are restarted by Docker indefinitely", so
rebuilding those "would create an infinite loop".

https://github.com/dokku/dokku-event-listener/blob/a897b9597cca57d23c3e7b6b14e7042a0e77445a/commands/watch.go

Note the restart policy that makes this work. Dokku's default is `on-failure:10`, not
`unless-stopped` (`DefaultProperties["restart-policy"] = "on-failure:10"` in
`plugins/ps/ps.go`). A ceiling is what converts an invisible infinite loop into an event
something can act on. #586's `unless-stopped` choice is the one that removes that signal.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/ps/ps.go

Because there is a ceiling, restart policy alone cannot survive a reboot, so dokku installs
`dokku-redeploy.service` (systemd, `WantedBy=docker.service`) running `dokku ps:restore
--parallel -1` at boot, with an Upstart fallback. The docs are explicit: "Restart policies
have no bearing on server reboot, and Dokku will always attempt to restart your apps at that
point."

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/00_dokku-standard/install

Dokku is also the system that proves the cron point. It installs, on the box:
`dokku-retire` on a systemd timer every five minutes (`OnCalendar=*:0/5`, with an
`/etc/cron.d/dokku-retire` fallback where systemd is absent), `docker-builder-prune` daily
at 04:05, and, for TLS, `letsencrypt:cron-job --add` writing `@daily …/letsencrypt/cron-job`
into the dokku user's own crontab. There is no cron for general cleanup, because
`docker_cleanup` runs synchronously inside `dokku_receive()` on every push, the way kamal
prunes at the end of every deploy.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/scheduler-docker-local/install
https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/builder/install-builder-prune
https://github.com/dokku/dokku-letsencrypt/blob/bd8987d8a5ef4c019ba5c68ee2395e35b631abe5/internal-functions

Its deploy-time probe is the third distinct answer in this survey, and it is host-side. A Go
binary, `docker-container-healthchecker`, is invoked with `sudo` from the `check-deploy`
trigger; on failure it runs `docker container update --restart=no` and stops the new
container, then aborts the deploy. The docs say "Web checks are performed via `curl` on
Dokku host", with defaults of 5s wait, 30s timeout and 5 attempts. Zero-downtime works by
waiting, switching proxy traffic once checks pass, then waiting `wait-to-retire` (default
60s) before killing the old containers.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/scheduler-docker-local/check-deploy
https://dokku.com/docs/deployment/zero-downtime-deploys/

Logs are `docker logs` per container with no daemon in between, same as kamal, with one
addition worth knowing: `logs:set <app>|--global vector-sink <dsn>` plus
`logs:vector-start` runs a Vector container using Vector's `docker_logs` source. That is the
log-shipping capability solved by a third-party container rather than by dokku's own agent.
No metrics shipping exists in core.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/logs/functions.go
https://dokku.com/docs/deployment/logs/

### 1.6 CapRover: let docker be the agent

CapRover's answer is to not write one. It runs `swarmInit` on install (`DockerApi.initSwarm`,
called from `CaptainInstaller`), and its own controller is a swarm service named
`captain-captain` on the leader node. Adding a worker is the literal command `docker swarm
join --token <token> <ip>:2377 --advertise-addr <workerIp>:2377` sent over SSH. Nothing
CapRover-specific is installed on a worker. The resident agent on every node is `dockerd`
itself, in swarm mode.

https://github.com/caprover/caprover/blob/c352c9785464caf75166274867f00bc054fa6ce2/src/docker/DockerApi.ts
https://caprover.com/docs/app-scaling-and-cluster.html

Deploy gating is thinner than it looks. CapRover sets no `HEALTHCHECK` of its own, and sets
no explicit `RestartPolicy` on the services it creates, so both fall through to docker's
defaults. It does set `UpdateConfig.Order`, choosing `stop-first` when the service has
volume mounts and `start-first` otherwise. Its readiness check polls
`listTasks({filters: {service, 'desired-state': ['running']}})` up to ten times at three
second intervals. That confirms the task was scheduled, not that the app answers. Real
health gating during the rolling update is left to whatever `HEALTHCHECK` the user's own
Dockerfile happens to declare, which swarm consumes natively.

I could find no primary statement of why swarm was chosen over an agent, in the docs or the
repo. Treat the rationale as unverified. The mechanism is not: adopting an orchestrator
means adopting its agent and its opinions, including the opinion that a service with volumes
cannot start-first.

### 1.7 flyd: the opposite pole, and what it costs

fly.io's answer is a per-host daemon that is authoritative.

> flyd is the source of truth for all the VMs running on a particular worker.

> Every flyd keeps a boltdb database of its current state, which is an append-only log of
> all the operations applied to the worker.

> Each [worker] is its own source of truth.

The central component shrinks to a proxy over those daemons, "flaps is a stateful proxy for
all the flyd instances".

https://fly.io/blog/carving-the-scheduler-out-of-our-orchestrator/

Worth naming because it is the far end of the spectrum. Durable local state, an append-only
operation log, a finite state machine per operation. That is the price of an agent that
actuates, and fly pays it because their operator is an API rather than a human with an SSH
session. Nobody is around to drive the box. Ocel's operator today is a human running
`ocel deploy`.

### 1.8 docker's restart policy, and what it does not cover

- `unless-stopped`: "Similar to `always`, except that when the container is stopped
  (manually or otherwise), it isn't restarted even after Docker daemon restarts."
- "A restart policy only takes effect after a container starts successfully. In this case,
  starting successfully means that the container is up for at least 10 seconds and Docker
  has started monitoring it."
- "Don't combine Docker restart policies with host-level process managers, as this creates
  conflicts."

https://docs.docker.com/engine/containers/start-containers-automatically/

Backoff, from the `docker run` reference. "An increasing delay (double the previous delay,
starting at 100 milliseconds) is added before each restart to prevent flooding the server…
until either the `on-failure` limit, the maximum delay of 1 minute is hit", and the delay
resets once a container "is started and runs for at least 10 seconds". `always` and
`unless-stopped` carry no retry ceiling.

https://docs.docker.com/reference/cli/docker/container/run/

So a crash loop has a one-minute floor, no end, and no notification. A reasonable default,
and also the reason crash-loop alerting is a different capability from crash-loop survival.
Docker handles survival by never giving up, which is what makes silence indistinguishable
from health.

The gap that matters more: a restart policy reacts to process exit and never to
`HEALTHCHECK` status. Restarting an unhealthy but running container has been an open
feature request on moby since 2016 and is still open, moby/moby#28400, "trigger restart from
unhealthy status".

https://github.com/moby/moby/issues/28400

The failure mode agentless supervision cannot see is the interesting one. The process is
alive, the container is `running`, the app is wedged on a deadlocked pool or exhausted file
descriptors. Docker is content. On a kamal-shaped box the proxy stopped probing, so the
proxy is content too.

## 2. Capability by capability

| Capability | Drivable from occasional CLI-over-SSH? | What the prior art does instead | Where it visibly breaks |
| --- | --- | --- | --- |
| Deploy-time health gate | Yes | Four different answers. kamal: the resident proxy probes, CLI reads the exit code. Dokku: `docker-container-healthchecker` run host-side under sudo. Coolify: control plane SSH-polls `docker inspect` health. CapRover: polls swarm for a task in `desired-state: running`, which is not health at all | When the prober's path is not the traffic path (section 3). CapRover's version can report success for an app that never answers a request |
| Continuous probing between deploys | No. An occasional session is by definition not continuous | kamal-proxy stops checking a single target. Docker restarts on exit only | Wedged but running app serves 502s until a human looks. Neither docker nor the proxy notices |
| Crash-loop detection | Yes, via `docker inspect .RestartCount`, but only while connected | Dokku sets `on-failure:10` so the loop has a ceiling and produces an event. Coolify polls `RestartCount` every minute | With `unless-stopped` there is no ceiling and therefore no event. An app that dies on boot every 20 seconds is down indefinitely and the last deploy reported success |
| Crash-loop alerting and recovery | No | Dokku's `dokku-event-listener` watches the docker event stream and runs `ps:rebuild` past the retry ceiling. Coolify's control plane stops the app past `max_restart_count` and notifies the team | Nobody is told. The gap is not detection, it is that no process is awake to speak. Closing it without a host agent means a permanently-up control plane holding SSH keys to every box |
| Log shipping | No, pull only | `docker logs` over SSH in both kamal and dokku. Dokku adds an optional Vector container fed by Vector's `docker_logs` source | Post-mortem after the container was pruned. json-file rotation silently drops the window you need |
| Metrics | No | kamal-proxy's opt-in Prometheus endpoint, scraped by someone else. Coolify gave up and shipped Sentinel | Needs inbound reachability into the box or an outbound pusher. The strongest agent-forcing item on the list |
| Scheduled GC and retention | Partly. Prune at the end of each deploy covers the active box | kamal and dokku both prune inside the deploy. Dokku additionally installs systemd timers: `dokku-retire` every five minutes, `docker-builder-prune` daily | A box that has not deployed in months fills its disk. Worse, GC that runs only on success never runs on the box whose deploys fail because the disk is full |
| Boot-time reconciliation | No, nobody is connected at boot | kamal: `unless-stopped` plus a proxy that restores its own snapshot. Dokku: a systemd unit running `ps:restore` at boot, because `on-failure` does not survive reboot | kamal's proxy marks restored targets healthy without probing, so a broken app after reboot still receives traffic |
| Cert renewal | No | kamal-proxy renews itself via `golang.org/x/crypto/acme/autocert`, at the lesser of 30 days or a third of the certificate lifetime before expiry. Dokku writes an `@daily` crontab entry | Only when the renewing process is down for weeks, by which point the site is down anyway. Renewal rides on something that has to be up regardless |

Three conclusions fall out of the table.

A cron line is not an agent, and dokku proves it at production scale. Scheduled GC,
retirement and cert renewal all run there as systemd timers or crontab entries that shell
out to a binary already on the box. Unattended, on-box, and with no daemon, no state, no
protocol and no supply chain of their own. Treating "must run when nobody is connected" as
"needs an agent" overcounts the agent case badly.

A resident proxy is already an agent for probing purposes. The distinction that survives
scrutiny is not resident versus not. It is what the resident process may do. The proxy
observes and routes. It never pulls images, never writes container config, never holds
deploy credentials. That boundary is worth keeping.

The restart policy choice decides whether crash looping is observable at all. `unless-stopped`
retries without a ceiling, so a crash loop emits no terminal event and looks exactly like
health. Dokku's `on-failure:10` gives up, which is what its event listener listens for, and
the cost is that a reboot then needs `ps:restore`. #586 has provisionally picked
`unless-stopped`. That is defensible, and it should be picked knowingly, because it trades
the crash-loop signal for reboot simplicity.

## 3. Recommendation: who probes at deploy time (#586 Q3)

The proxy probes. The CLI issues one addressable call and blocks on the result.

Three grounds.

The probe has to exercise the traffic path. A CLI-over-SSH probe reaches the container from
the host's shell, through a published port or `docker exec … curl`. The proxy reaches it
over the docker network the way a real request will. When those paths disagree, because the
container is on the wrong network, or binds `127.0.0.1` inside the container, or the proxy
cannot resolve the container name, a CLI probe that passes is a lie and the flip promotes a
target that cannot serve.

Gate and flip should be one operation. CLI-side probing makes it two SSH round trips with a
gap, and anything that changes in the gap, such as the container dying right after the
probe passes, flips traffic onto a dead target. kamal-proxy's `deploy` gates, flips and
drains the old target inside one call, which is why kamal can promise that removing the old
instance is safe as soon as `deploy` returns.

It costs nothing. #586 already commits to a resident reverse proxy. Probing is capability
that proxy has whether or not Ocel uses it.

One thing this argument is not. It is not about latency, and dokku settles that. Dokku probes
with `curl` on the dokku host, driven by the same shell session that is doing the deploy, and
it works fine. An `ssh host curl …` probe runs on the box, not across the internet from the
operator's laptop, so round-trip time is not the problem. The problem is path fidelity and
atomicity, which is why the ranking is proxy first, host-side CLI probe second, control-plane
poll third, and CapRover's "is a task scheduled" last.

The alternative, and why it comes second. Coolify does gate from the control plane over SSH,
polling `docker inspect --format='{{json .State.Health.Status}}'` for the length of the
deploy. It works, and it costs two things. It needs the container to have a healthcheck at
all, which for an arbitrary user image means injecting one, and coolify generates a compose
`healthcheck` whenever the Dockerfile declares none, which is a guess about an app it does
not understand. It also moves the definition of "up" away from the thing traffic will hit
and onto the thing docker was told to run. Ocel already injects `PORT`. An HTTP probe
against that port is a fact Ocel owns rather than a guess, and the proxy is already sitting
in the right place to make it.

What stays with the CLI, unchanged from #586's round-1 agreements: choosing the image and
target, setting the timeout, pulling the failed container's logs into the failure output,
and removing the failed container so there is never a two-container end state. The CLI also
has to check the proxy itself over SSH before trusting it. kamal does this with a version
gate that reads the running container's image tag and refuses to proceed when it is older
than a compiled-in minimum, rather than silently replacing a running proxy
(`lib/kamal/cli/proxy.rb`).

One conflict to hand back to #588. #586's round-1 definition of "up" is any HTTP response on
the injected `PORT`. kamal-proxy does not offer that. It treats anything outside 200 to 299
as a failed check.

```go
if resp.StatusCode < 200 || resp.StatusCode > 299 {
    hc.reportResult(false, fmt.Errorf("%w (%d)", ErrorHealthCheckUnexpectedStatus, resp.StatusCode))
```

https://github.com/basecamp/kamal-proxy/blob/fb0f36c386b4bbcb561c936cc10baf2ae497a656/internal/server/health_check.go

If proxy-side probing is adopted, the acceptable-status set becomes a proxy selection
criterion in #588 rather than a free choice. Either the chosen proxy can be told which
statuses count, or "up" narrows to whatever that proxy means by healthy. Deciding "any
response" and "the proxy probes" independently would leave a contradiction on the floor.

## 4. Smallest honest agent scope, and whether agentless now forecloses it

### 4.1 The smallest agent worth shipping

One container, `--restart unless-stopped` like everything else on the box, with four
properties.

Outbound only. It dials the console, opens no port, and needs no firewall change. Inbound
reachability is what turns a VPS into an attack surface, and it is also what would otherwise
force an agent for the wrong reason, namely metrics scraping.

Reporter, never actuator. It reads the local docker event stream, container state and
`RestartCount`, tails logs, scrapes the proxy's local `/metrics`, and forwards. It does not
pull images, does not start or stop app containers, holds no registry or cloud credentials,
and carries one token scoped to ingest for one project.

Off the deploy path. If it is dead, `ocel deploy` still works end to end. The moment a
deploy blocks on the agent being alive, the agent becomes a second thing to bootstrap,
version and repair over SSH, and repairing it needs the CLI anyway, so the dependency
becomes circular.

No durable authoritative state. The console is the record and the box is a source of
observations. Once the agent owns desired state there are two writers, CLI and agent, which
means leases, reconciliation and an operation log. That is the flyd shape, and section 1.7
shows its cost.

The precedents line up on that boundary. Coolify's Sentinel is a reporter. flyd is an
actuator and pays the full price.

Dokku sits in between and is worth arguing with, because `dokku-event-listener` is an
actuator and it is tiny. One input, the docker event stream. One condition, `RestartCount`
past the policy's ceiling. One action, `dokku ps:rebuild <app>`. It stays small because it
delegates the action back to the full control surface, which on a dokku box is already
installed. That is the property Ocel does not have. The `ocel` binary runs on the operator's
laptop, so an Ocel actuator would either reimplement the deploy path on the box or install
the CLI there, and both are considerably more than "one container that reports". Until that
changes, reporter-only is the honest scope, and dokku's shape is what to copy on the day
Ocel does put a control surface on the box.

Coolify's design document for the actuator they have not shipped draws the same line in
almost these words, and it is the boundary Ocel would inherit.

> If coold grows app-aware behavior, it becomes a second control plane with local copies of
> product rules. If Coolify reaches around coold with raw host access, the privileged
> boundary disappears and host behavior becomes harder to validate.

> coold must not own Coolify product concepts. It does not decide what an application,
> project, team, deployment, domain, rollback, billing event, or audit record means.

It also lists this negative consequence: "New deploy features may require new coold
primitives before they can ship."

https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/docs/v5/architecture/adr/0002-coold-host-agent-boundary.md

That last line is the whole tax. Once an agent sits on the deploy path, every new deploy
capability becomes a protocol change plus a rollout of a binary living on customer hardware,
versioned and upgraded and repaired over the same SSH the agent was meant to replace. A
reporter never charges that tax, because nothing waits on it.

### 4.2 Does going agentless now foreclose the agent?

No, as long as the cutover contract has three properties. All three are things kamal already
does, so none of it is speculative.

Desired state lives in files on the box, not only in the CLI's memory. kamal reads the
proxy's boot options off the host, `cat .kamal/proxy/options` piped into `xargs docker run`,
so the host file is the record and "reset" means deleting the file. Anything that can read a
file can later be the driver.

The flip is one idempotent, addressable operation against the resident proxy, not a bespoke
sequence of shell steps. `kamal-proxy deploy <service> --target host:port` is a call an
agent could later issue verbatim. A hand-rolled multi-step SSH script is what forecloses,
because it leaves no seam to hand over.

Container labels are the join key. If containers carry service, role and version labels, any
observer rebuilds state from `docker ps` without a private ledger. A future agent then needs
no migration and no handshake with the CLI. It reads the same box the CLI reads.

What would foreclose it, worth writing down as a constraint on #586's implementation:

- Deploy state that exists only in the console database or only in CLI-local files, with
  nothing on the box able to answer what is running and why.
- A cutover expressed as an ordered SSH script rather than a call. With no single entry
  point an agent has to reimplement the sequence, and the two implementations drift.
- Any design that needs inbound reachability to the box. That is a firewall and auth
  commitment made for a capability, metrics or remote control, that an outbound agent would
  serve better, and it is hard to walk back.

## 5. Named fog: capabilities that would force an agent

Ordered by how likely each is to arrive, so the map can hold them as named fog rather than
as a surprise.

1. Push log shipping to the console. `docker logs` over SSH is pull, interactive, and dies
   with the container. The nearest agentless substitute is a remote docker log driver, which
   is configuration rather than a daemon, and should be tried first. It fails when the sink
   needs auth refresh, buffering across a network partition, or per-app routing.
2. Continuous metrics. No outbound path exists without a resident process, and inbound
   scraping is the only alternative and is worse. Coolify reached this item and built
   Sentinel. This is the one most likely to force the decision.
3. Crash-loop alerting between deploys. Detection is trivial. Being awake is not. Two
   cheaper interims exist before an agent. Keep the resident proxy health-checking and log
   the state transition, since kamal-proxy already logs "Target health updated", then have
   the next CLI session surface it, which delays the alert to the next deploy. Or take
   dokku's route and give the restart policy a ceiling so the loop terminates into an
   observable state instead of grinding silently. Both are worth exhausting first, and both
   still leave the alert waiting for someone to look.
4. Deploys not initiated by an operator's SSH session: console-triggered, git-push, webhook.
   Something on the box has to pull, or something has to hold a persistent inbound
   connection. This is the capability that turns the reporter into an actuator, and it is
   the point at which the agent decision genuinely reopens.
5. Self-healing beyond `--restart`: restarting on unhealthy, backoff ceilings, automatic
   rollback after N crashes. Docker will not do it, moby/moby#28400 is still open, and the
   proxy can only stop routing rather than restart. Two prior-art versions exist, and they
   are the cheapest actuator in the survey and the most expensive control plane in it.
   Dokku's event listener acts locally; coolify's poller acts remotely and needs SSH keys to
   every box to do it.
6. Multi-host for one app. Several boxes behind one hostname need shared knowledge of which
   target is live, and the proxy's per-box snapshot stops being enough. CapRover's answer is
   to adopt swarm, which means adopting swarm's agent and its opinions wholesale.
7. Box-side retention and rollback state. #586 leaves open whether re-promoting re-transfers
   the image or relies on box-side retention. If retention is the answer, something has to
   enforce depth while the CLI is not connected. A cron line covers that until retention has
   to be reconciled with the console's ledger.
8. Unattended host maintenance: docker engine upgrades, disk-pressure response, unattended
   reboots. Cron covers the sweep. Only the judgement calls need an agent.
9. Secrets rotation on the box. Rotating without a deploy means something has to fetch and
   restart. Today rotation is a deploy, which is the cheapest possible answer and worth
   keeping.

Items 1 to 3 are reporter-shaped and share one agent. Item 4 changes the agent's nature.
Items 5 to 9 mostly do not need an agent as long as a cron line and the resident proxy stay
on the table.
