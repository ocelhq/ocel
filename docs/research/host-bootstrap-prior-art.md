# Host bootstrap prior art: what kamal, dokku and coolify actually create on a fresh box

Sources pinned to the commits read: kamal `eee0083b38661c3707c6b6052cc89e85038a096c`, dokku `c554cd40adb7de828dee528349bca4df5190e685`, coolify `44e4f071b1e3340c516cab2cc6d8c4738d47087d`, dokku/sshcommand `b77824035ea33d1da8d660dc10d40665f0bbac8d`.

## Short answer

- Kamal's whole host bootstrap is **one thing: make `docker` exist**. `kamal server bootstrap` checks `docker -v`, and if that fails pipes `https://get.docker.com` into `sh`. It creates no directory, no user, no unit, no config file. Everything else appears lazily during deploy.
- All three converge on the same docker-install mechanism — the upstream convenience script at `get.docker.com` — with coolify keeping a per-distro apt/dnf fallback and dokku wrapping it in `wget … | sh`. Nobody vendors packages themselves.
- Kamal's on-host state is a **single tree, `~/.kamal`, relative to the SSH login user's home**, whose default user is `root`. Inside: `apps/<service>[-<dest>]/env/roles/<role>.env`, `apps/…/assets`, `proxy/{options,image,image_version,run_command,apps-config/…}`, `lock-<service>[-<dest>]/`, `<service>[-<dest>]-audit.log`. All plain files, all created on demand with `mkdir -p`.
- Kamal never manages SSH host keys. It configures Net::SSH through SSHKit with user/port/keys/proxy/forward_agent and nothing about `known_hosts` or `verify_host_key` — host-key trust is inherited from the operator's own SSH setup.
- Dokku is the opposite pole: an OS package (`packagecloud.io` apt repo) whose `postinst` creates a **dedicated `dokku` user** in the `docker` group, `/home/dokku` (SSH surface + per-app git repos) and `/var/lib/dokku` (plugins + `config/<plugin>/<app>/<key>` property files). No database — the state is the filesystem.
- Dokku's user-facing auth is `authorized_keys` **forced commands**: every operator key is written with `command="FINGERPRINT=… NAME=… /usr/bin/dokku $SSH_ORIGINAL_COMMAND",no-agent-forwarding,no-user-rc,no-X11-forwarding,no-port-forwarding`. The `dokku` user is not a login shell in practice.
- Coolify demands **root**, takes `/data/coolify/*` wholesale (chowned to uid `9999`, mode `700`), rewrites `/etc/docker/daemon.json`, generates an ed25519 key and appends it to **root's own `authorized_keys`** so the container can SSH back into its own host. State is a **Postgres container** (`coolify-db` volume) plus a `.env` file of generated secrets.
- Nobody offers a plan-before-mutate step. Idempotence is per-step and ad hoc: kamal via "is docker installed", dokku via `dpkg` + `command -v`, coolify via existence checks (`.env` merge, `docker volume ls | grep coolify-db` standing in for "already installed"). The only consent UX anywhere is coolify's and dokku's `sleep 10` before a destructive step.

## 1. Kamal

### 1.1 `setup` = bootstrap + deploy

`kamal setup` is two invocations: `kamal:cli:server:bootstrap`, then `deploy(boot_accessories: true)`. Nothing else touches the host.
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/main.rb

`bootstrap` in full, per host:

1. `docker -v` — if it succeeds, do nothing at all.
2. else check superuser: `[ "${EUID:-$(id -u)}" -eq 0 ] || sudo -nl usermod >/dev/null`.
3. install: `curl -fsSL https://get.docker.com | sh`, falling back to `wget -O - https://get.docker.com | sh`, falling back to `echo "exit 1"`.
4. if not root and not already in the docker group, `sudo -n usermod -aG docker "${USER:-$(id -un)}"` then `kill -HUP $PPID` to refresh the session.
5. run the `docker-setup` hook (a **local** script, not remote).

Hosts where docker is missing *and* there is no root/sudo are collected and the command raises with "Install Docker manually".
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/server.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/docker.rb

That is the entire host bootstrap. No directories, no users, no systemd, no config files.

### 1.2 Who it runs as

Default SSH user is `root`, port 22:

```ruby
def user
  ssh_config.fetch("user", "root")
end
```

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/ssh.rb

Non-root is supported, but only if that user has passwordless sudo (`sudo -nl usermod`) or docker is already installed and the user is already in the `docker` group. Kamal never creates a user of its own.

### 1.3 What ends up on the host, and when

`run_directory` is the literal relative string `".kamal"` — i.e. resolved against the SSH login user's home directory by the remote shell, never an absolute path.

```ruby
def run_directory   = ".kamal"
def apps_directory  = File.join(run_directory, "apps")
def app_directory   = File.join(apps_directory, service_and_destination)
def env_directory   = File.join(app_directory, "env")
def assets_directory= File.join(app_directory, "assets")
```

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration.rb

Created lazily, each by `mkdir -p` (`Kamal::Commands::Base#make_directory`):

| Path | Created by |
| --- | --- |
| `.kamal/apps/<svc>[-<dest>]/env/roles/` | `app boot` → `ensure_env_directory` |
| `.kamal/apps/<svc>[-<dest>]/env/roles/<role>.env` | uploaded, passed to docker as `--env-file` |
| `.kamal/apps/<svc>[-<dest>]/assets/` | `app assets` |
| `.kamal/proxy/` | `proxy boot_config set` → `ensure_proxy_directory` |
| `.kamal/proxy/{options,image,image_version,run_command}` | `proxy boot_config set` (each reset by deleting the file) |
| `.kamal/proxy/apps-config/<svc>[-<dest>]/{tls,error_pages}/` | `proxy boot`, `app boot` |
| `.kamal/lock-<svc>[-<dest>]/details` | deploy lock |
| `.kamal/<svc>[-<dest>]-audit.log` | every audited command, appended |

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/base.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/role.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/proxy/boot.rb
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/auditor.rb

The deploy lock is a **`mkdir` used as a mutex** — `mkdir <dir>` (no `-p`) fails if it exists, and lock metadata (who, when, version, message) is base64'd into `<dir>/details`:

```ruby
def acquire(message, version)
  combine [ :mkdir, lock_dir ], write_lock_details(message, version)
end
```

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/lock.rb

The one docker object created outside a directory is the network: `docker network create kamal`, run at `proxy boot` and `accessory boot` with the `already exists` failure swallowed.
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/proxy.rb

### 1.4 kamal-proxy install

There is no install. `kamal proxy boot` `docker run`s `basecamp/kamal-proxy` detached with `--restart unless-stopped` on the `kamal` network, mounting a named volume `kamal-proxy-config:/home/kamal-proxy/.config/kamal-proxy` and bind-mounting `.kamal/proxy/apps-config` into `/home/kamal-proxy/.apps-config`. Container name is the fixed string `kamal-proxy`.
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/commands/proxy.rb

Two convergence details worth stealing:

- **Boot options are read off the host, not pushed.** `boot_config` is `echo $(cat .kamal/proxy/options || echo "<defaults>") $(cat image || echo basecamp/kamal-proxy):$(cat image_version || echo MINIMUM_VERSION) $(cat run_command)` piped into `xargs docker run …`. The host file *is* the record; "reset" means `rm` the file and fall back to the compiled-in default.
- **Version gate, not reinstall.** `boot` reads the running container's image tag (`docker inspect --format '{{.Config.Image}}' | awk -F: '{print $NF}'`) and raises "run `kamal proxy reboot`" if it is older than `MINIMUM_VERSION`. It never silently replaces a running proxy. Start is `docker container start kamal-proxy || docker run …`.

https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/proxy.rb

### 1.5 SSH and host keys

`Kamal::Configuration::Ssh#options` is the whole surface handed to Net::SSH via SSHKit:

```ruby
{ user:, port:, proxy:, logger:, keepalive: true, keepalive_interval: 30,
  keys_only:, keys:, key_data:, config:, forward_agent: }.compact
```

`proxy` becomes `Net::SSH::Proxy::Jump` (defaulting the jump user to `root`) or `Net::SSH::Proxy::Command`. `key_data` may be pulled from kamal secrets; inline `key_data` is deprecated for Kamal 3. Default `log_level` is `:fatal`.
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/ssh.rb

**`verify_host_key`, `known_hosts` and `StrictHostKeyChecking` do not appear anywhere in kamal's source.** Host-key policy is entirely Net::SSH's default against the operator's `~/.ssh/known_hosts`, optionally shaped by `ssh.config` (which OpenSSH config files to read). A first-contact host is the operator's problem, not kamal's.

The concurrency knobs live separately: `max_concurrent_starts: 30`, `pool_idle_timeout: 900`, `dns_retries: 3`.
https://github.com/basecamp/kamal/blob/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/configuration/sshkit.rb

### 1.6 Local vs host state

Local, in the app repo: `config/deploy.yml`, `.kamal/secrets`, `.kamal/hooks/*` (sample hooks: `pre-connect`, `pre-build`, `pre-deploy`, `post-deploy`, `pre/post-app-boot`, `pre/post-proxy-reboot`, `docker-setup`). Hooks run **locally**, on the operator's machine.
https://github.com/basecamp/kamal/tree/eee0083b38661c3707c6b6052cc89e85038a096c/lib/kamal/cli/templates/sample_hooks

Host: only the `~/.kamal` tree above, plus docker's own state (containers, images, the `kamal` network, the `kamal-proxy-config` volume). Note the collision of names — `.kamal/hooks` and `.kamal/secrets` are *local* paths under the same dot-name as the *remote* run directory.

## 2. Dokku

### 2.1 Install

`bootstrap.sh` is meant to be `sudo`-run on Debian 11–13 / Ubuntu 22.04–24.04. It refuses without a resolvable `hostname -f`, warns under ~1 GB RAM, installs `gpg-agent`/`software-properties-common`/`lsb-release`, then installs the **deb package** from packagecloud:

```sh
wget -qO- https://packagecloud.io/dokku/dokku/gpgkey | sudo tee /etc/apt/trusted.gpg.d/dokku.asc
echo "deb https://packagecloud.io/dokku/dokku/$DOKKU_DISTRO/ $OS_ID main" | tee /etc/apt/sources.list.d/dokku.list
apt-get -qq -y install dokku
```

Docker comes first, same convenience script, only if absent: `command -v docker || (export CHANNEL=stable; wget -nv -O - https://get.docker.com/ | sh)`. Answers can be pre-seeded through debconf (`DOKKU_HOSTNAME`, `DOKKU_VHOST_ENABLE`, `DOKKU_KEY_FILE`, `DOKKU_NGINX_ENABLE`, `DOKKU_SKIP_KEY_FILE`) — that is dokku's non-interactive path. A source install (`git clone` into `/root/dokku`, `make install`) still exists for pinned branches.

First-time installs print a warning that nginx `sites-enabled` files will be removed and then `sleep 10` — the only "are you sure" in the whole script.
https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/bootstrap.sh

### 2.2 What `postinst` creates

Roots are overridable via `/etc/default/dokku`: `DOKKU_ROOT=${DOKKU_ROOT:-/home/dokku}`, `DOKKU_LIB_ROOT=${DOKKU_LIB_PATH:-/var/lib/dokku}`.

`setup-user`:
```sh
sshcommand create dokku /usr/bin/dokku
grep -i -E "^docker" /etc/group || groupadd docker
usermod -aG docker dokku
mkdir -p "$DOKKU_ROOT/.ssh" "$DOKKU_ROOT/.dokkurc"
touch "$DOKKU_ROOT/.ssh/authorized_keys"
chown -R dokku:dokku "$DOKKU_ROOT/.ssh" "$DOKKU_ROOT/.dokkurc"
```

`setup-storage`: `mkdir -p /var/lib/dokku/data /var/lib/dokku/data/storage`, `chown dokku:dokku`.

`setup-plugins`: `mkdir -p /var/lib/dokku/{core-plugins,plugins}/{available,enabled}`, `touch …/config.toml` for each, symlink every core plugin from `core-plugins/available` into `plugins/available`, `plugn enable` it in both trees, prune broken symlinks, `chown dokku:dokku -R`, then `dokku plugin:install --core`.

`setup-sshcommand`: writes `/usr/bin/dokku` into `$DOKKU_ROOT/.sshcommand`.

`setup-docker-live-restore`: `jq '. + {"live-restore": true}'` into `/etc/docker/daemon.json` (creating `{}` first if absent) and `systemctl reload docker` — skipped when `DOKKU_INIT_SYSTEM=sv` or `DOKKU_LIVE_RESTORE=false`, and skipped when `docker info --format '{{ .LiveRestoreEnabled }}'` already says true.

`setup-default-site`: installs an nginx catch-all at `/etc/nginx/conf.d/00-default-vhost.conf` (a legacy template when nginx < 1.19.4 lacks `ssl_reject_handshake`), first *renaming rather than deleting* conflicting defaults to `<path>.dokku-disabled`. Only on first configure (`[ -z "$2" ]`).

`dpkg-handling`: writes `$DOKKU_ROOT/VHOST` from the debconf hostname *unless the file already exists*, and adds the operator's key via `sshcommand acl-add dokku default`.

https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/debian/postinst

`postrm` shows the inverse inventory: `.dokkurc`, `dokkurc`, `tls`, `.ssh/authorized_keys`, `.sshcommand`, `ENV`, `HOSTNAME`, `VERSION`, `.cache`, then delete dangling symlinks and empty dirs under `$DOKKU_ROOT`.
https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/debian/postrm

### 2.3 The `dokku` user and forced commands

`sshcommand create dokku /usr/bin/dokku` creates the user (`useradd -m -s /bin/bash`, or `adduser` variants per distro), makes `~/.ssh/authorized_keys`, and stores the target binary in `~/.sshcommand`. Every key added by `sshcommand acl-add` is prefixed:

```
command="FINGERPRINT=<fp> NAME=\"<name>\" `cat ~/.sshcommand` $SSH_ORIGINAL_COMMAND",no-agent-forwarding,no-user-rc,no-X11-forwarding,no-port-forwarding <key>
```

Duplicate name and duplicate fingerprint are both rejected by default. Removal is a `sed` on the `NAME=` or `FINGERPRINT=` marker — the `authorized_keys` file is the ACL database, and the markers are its primary keys.
https://github.com/dokku/sshcommand/blob/b77824035ea33d1da8d660dc10d40665f0bbac8d/sshcommand

### 2.4 State storage

No database. Two trees:

- `/home/dokku/<app>/` — the bare git repo per app (`…/<app>/refs/heads/<branch>`), plus per-app files like `VHOST`.
  https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/git/report.go
- `/var/lib/dokku/config/<app>/ENV` and `/var/lib/dokku/config/--global/ENV` — app and global environment. Note the direction of travel: the pre-0.38 locations were `$DOKKU_ROOT/ENV` and `$DOKKU_ROOT/<app>/ENV`, and `MigrateEnvFiles` drains them into the config tree and deletes them. Dokku is consolidating state *out* of the user's home and into `/var/lib`.
  https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/config/environment.go
  https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/config/migrate.go
- `/var/lib/dokku/config/<plugin>/<app>/<key>` — every plugin property, one file per key, dirs `0755`:
  ```go
  func getPluginConfigPath(pluginName string) string {
    return filepath.Join(MustGetEnv("DOKKU_LIB_ROOT"), "config", pluginName)
  }
  ```
  https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/plugins/common/properties.go
- `/var/lib/dokku/data/<plugin>/…` and `/var/lib/dokku/data/storage` for persistent app volumes.

### 2.5 systemd

Dokku ships **no unit files** (`find` for `*.service`/`*.socket`/`*.timer` returns nothing in the repo). It rides docker's own service — it only `systemctl reload docker` after editing `daemon.json`, and containers get docker restart policies. Process supervision is docker's, not init's. The `DOKKU_INIT_SYSTEM=sv` escape hatch exists for the docker-in-docker image.

### 2.6 Plugin host footprint

A plugin is a directory under `/var/lib/dokku/plugins/available/<name>` with a `plugin.toml`, enabled by symlink into `enabled/` via `plugn`. Core plugins live in a parallel `core-plugins/` tree symlinked into `plugins/available`, so "core" and "user" plugins are distinguishable on disk and core ones can be replaced by a user plugin of the same name (the enable loop skips a name that already exists under `plugins/available`). `dokku plugin:install-dependencies --core` is what pulls per-plugin apt packages. Dokku's own binary dependencies (`sshcommand`, `plugn`, `procfile-util`, `sigil`, `docker-image-labeler`, …) are `wget`'d into `/usr/local/bin` and `chmod +x` by the Makefile / package deps.
https://github.com/dokku/dokku/blob/c554cd40adb7de828dee528349bca4df5190e685/Makefile

## 3. Coolify

### 3.1 Root, and immediately

```sh
if [ $EUID != 0 ]; then
  echo "Please run this script as root or with sudo"; exit
fi
```
https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/scripts/install.sh

A `# TODO: Ask for a user` sits directly above `CURRENT_USER=$USER` — running as a non-root user is not implemented.

### 3.2 Directories

```sh
mkdir -p /data/coolify/{source,ssh,applications,databases,backups,services,proxy,sentinel}
mkdir -p /data/coolify/images
mkdir -p /data/coolify/ssh/{keys,mux}
mkdir -p /data/coolify/proxy/dynamic
chown -R 9999:root /data/coolify
chmod -R 700 /data/coolify
```

Uid `9999` is the container's user; the host has no matching account. The install log is `tee`'d to `/data/coolify/source/installation-<date>.log`.

### 3.3 Docker

`curl -fsSL https://get.docker.com | sh` with `set +e` around it, and on failure a hand-rolled per-distro path (`install -m 0755 -d /etc/apt/keyrings`, `download.docker.com` gpg + repo, dnf/zypper/pacman equivalents). Snap-installed docker is a hard `exit 1`. Post-install it enforces `MIN_DOCKER_VERSION=24` from `docker version --format '{{.Server.Version}}'`.

It also owns `/etc/docker/daemon.json`: backs up the existing file to `daemon.json.original-<date>`, then writes `log-driver: json-file` with `max-size 10m` / `max-file 3` and `default-address-pools` (default `10.0.0.0/8` size 24), merging rather than clobbering unless `DOCKER_POOL_FORCE_OVERRIDE=true`, and skipping the daemon restart when the values already match.

### 3.4 State

- **Postgres in a container**, volume `coolify-db`, plus a `redis` container (volume `coolify-redis`) and a `soketi` realtime container. Compose files are downloaded from the CDN into `/data/coolify/source/`.
  https://github.com/coollabsio/coolify/blob/44e4f071b1e3340c516cab2cc6d8c4738d47087d/docker-compose.prod.yml
- **`/data/coolify/source/.env`** holds the generated secrets: `APP_ID` (`openssl rand -hex 16`), `APP_KEY` (`base64:$(openssl rand -base64 32)`), `DB_PASSWORD`, `REDIS_PASSWORD`, `PUSHER_APP_{ID,KEY,SECRET}`. On re-run the old `.env` is copied to `.env-<date>` and merged with `awk -F '=' '!seen[$1]++' "$ENV_FILE" ".env.production"` — **existing values win**, and `update_env_var` only writes a key that is missing or present-but-empty. That is coolify's entire idempotence story for secrets.
- The bind mounts are the rest of the state: `/data/coolify/{ssh,applications,databases,services,backups,images}` mapped into the app container's `storage/app/`.

### 3.5 SSH back into its own host

```sh
IS_COOLIFY_VOLUME_EXISTS=$(docker volume ls | grep coolify-db | wc -l)
if [ "$IS_COOLIFY_VOLUME_EXISTS" -eq 0 ]; then
  ssh-keygen -t ed25519 -a 100 -f /data/coolify/ssh/keys/id.$CURRENT_USER@host.docker.internal -q -N "" -C coolify
  chown 9999 …
  sed -i "/coolify/d" ~/.ssh/authorized_keys
  cat ….pub >>~/.ssh/authorized_keys
  rm -f ….pub
fi
```

The presence of the `coolify-db` docker volume is the "already installed" test. The private key stays on the host at `/data/coolify/ssh/keys/`, the public half goes into **root's** `authorized_keys` (identified for removal by the trailing `coolify` comment), and coolify manages its own host as just another SSH target through `host.docker.internal`. It also reports whether `sshd -T` shows `PermitRootLogin` enabled, but only warns.

## 4. Cross-cutting

### 4.1 Root vs dedicated user

| | runs as | dedicated user | docker group |
| --- | --- | --- | --- |
| kamal | SSH user, default `root` | none | adds the SSH user if non-root |
| dokku | `root` to install; `dokku` at runtime | `dokku` (`useradd -m -s /bin/bash`) | `dokku` added, group created if missing |
| coolify | `root`, enforced | none on the host (uid `9999` inside the container) | n/a — talks to the daemon socket |

Kamal is the only one that leaves the host's account layout untouched. Dokku's dedicated user pays for itself because the user *is* the API surface (forced-command SSH). Coolify's root requirement buys `/etc/docker/daemon.json`, `/data`, and a self-SSH key in root's `authorized_keys`.

### 4.2 Idempotence

Nobody has a convergence engine. Every step guards itself:

- kamal: `docker -v` gates the only mutation; `mkdir -p` everywhere else; `docker network create kamal` with the "already exists" error swallowed; `docker container start || docker run`; proxy version compared, not reinstalled. Re-running `kamal setup` on a live host is a no-op plus a deploy.
- dokku: `dpkg -l | grep -q <pkg>`, `command -v docker`, `command -v dokku`; `postinst` distinguishes first configure from upgrade via `[ -z "$2" ]`; `VHOST` is written only if absent; conflicting nginx defaults are **renamed, not deleted**, and skipped if a `.dokku-disabled` backup already exists; plugin enable skips names already present.
- coolify: `.env` merge keeps existing values; `daemon.json` diffed before restart and backed up before write; `docker volume ls | grep coolify-db` as the install marker; `sed -i "/coolify/d"` before appending its key so re-runs don't duplicate lines.

The shared shape: **an existence check per resource, expressed in the same imperative script as the mutation.** None of the three separates "what would change" from "change it".

### 4.3 Consent / plan-before-mutate

There is none, in any of the three. The closest anyone gets:

- dokku prints the nginx `sites-enabled` warning and `sleep 10` before a first install; a Linode-specific AUFS warning does the same.
- coolify prints a low-disk warning and `sleep 5`, prints a numbered "Step N/9" progress narration, and tells you the daemon is about to restart.
- kamal prints `say "Ensure Docker is installed..."` and `info "Missing Docker on #{host}. Installing…"` — after it has decided, not before.

Non-interactivity is the priority everywhere: `DEBIAN_FRONTEND=noninteractive` plus debconf pre-seeding in dokku, env-var overrides in coolify, and a config file in kamal.

### 4.4 "Is this host already set up?"

| | test |
| --- | --- |
| kamal | `docker -v` exit status. Nothing else — there is no kamal marker on the host until a deploy writes one. |
| dokku | `command -v dokku`, plus `dpkg`'s own package state; `$DOKKU_ROOT/VHOST` as a "configured" marker |
| coolify | `docker volume ls \| grep coolify-db`; existence of `/data/coolify/source/.env` |

Kamal's answer is the interesting one for Ocel: it deliberately has **no host identity record**. The host is fungible; everything kamal needs to know lives in the local `deploy.yml`, and the host is queried through docker (`docker ps --filter`, `docker inspect`) rather than through a state file kamal wrote. The only host-side records it keeps are the ones docker cannot answer: the proxy's boot options and the deploy lock.

## 5. What this suggests for Ocel's `vps bootstrap`

Reading only, no decisions taken — flagged as the branch points for the design ticket.

1. **Docker install mechanism.** All three use `get.docker.com`. Kamal's exact shape — `docker -v` → superuser probe → `curl … | sh` with a `wget` fallback → group-add + `kill -HUP $PPID` — is the minimum viable version and has the clearest failure message ("can't be automatically installed without having root access and either `wget` or `curl`").
2. **Root vs dedicated user.** A dedicated user is only worth its cost if, as in dokku, the user is also the auth surface. If Ocel drives the host over the operator's own SSH, kamal's "whatever user you gave us, default root" is strictly less to own.
3. **Directory root.** Kamal's relative `.kamal` (resolves under the login user's home) vs coolify's absolute `/data/coolify` vs dokku's split `/home/dokku` + `/var/lib/dokku`. The relative form makes non-root work without a second code path; the absolute form makes multi-operator hosts coherent.
4. **Host records.** Kamal keeps three kinds and no more: a lock (`mkdir` as mutex + a details file), a proxy boot record read back with `cat … || echo <default>`, and an append-only audit log. Everything else is recomputed from docker. The `cat file || echo default` idiom means "unset" and "set to the default" are the same state, which is what makes reset a plain `rm`.
5. **Identifiers.** Kamal's names are fixed and unnamespaced — network `kamal`, container `kamal-proxy`, volume `kamal-proxy-config` — with per-app scoping pushed down to `service_and_destination` path segments. Coolify's are `coolify-db`/`coolify-redis`. Dokku's are the `dokku` user and the app name. Whatever Ocel picks becomes a permanent collision surface on a shared host.
6. **Consent UX is an open lane.** No prior art here to copy or contradict — a plan-before-mutate step would be a differentiator rather than a deviation, and Trust > Product argues for it on a box the customer already owns.
