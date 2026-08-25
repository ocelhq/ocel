# The Go SSH stack: `x/crypto/ssh`, `knownhosts`, ssh_config, agents

Ticket: ocelhq/ocel#562 (map: #561). Sources are the packages' own code and godoc, the OpenSSH man
pages and source, and the Go issue tracker. Claims marked **(probed)** were run against
`golang.org/x/crypto v0.54.0` and `github.com/kevinburke/ssh_config v1.6.0`; both are in the local
module cache under `$(go env GOMODCACHE)`.

## Short answer

- **`knownhosts.KeyError.Want` does not separate TOFU from MITM.** `Want` is empty only when no
  line matched the address at all. It is non-empty, meaning "key mismatch", whenever any line
  matched the host. That includes the case where the server merely offered a key *type* we have no
  entry for, and the case where the only matching line is an `@cert-authority` line. First contact
  with a host that has a `@cert-authority` entry reports MITM, not unknown.
- **The fix is blocked upstream and has one known workaround.** OpenSSH reorders
  `HostKeyAlgorithms` to prefer the algorithms it already has keys for. x/crypto does not, and
  `knownhosts` exports no way to enumerate a host's keys: godoc lists `New`, `Normalize`, `Line`,
  `HashHostname` and three types, nothing else. That is golang/go#29286, open since 2018. The only
  workaround is `skeema/knownhosts`, which recovers the key list by calling the callback with a
  bogus key and reading `KeyError.Want`.
- **The address string carries the whole lookup, and it is unforgiving.** The callback's `hostname`
  argument is verbatim the string passed to `ssh.Dial`. `knownhosts` calls `net.SplitHostPort` on it
  and returns a plain error, not a `*KeyError`, when there is no port. `[host]:port` is required for
  any port but 22, and an entry written for `:2222` does not match `:22` or the reverse. It fails
  silently, as "unknown host".
- **x/crypto never writes known_hosts.** `Line` returns one line, with no newline, unhashed. There
  is no writer, no hashed-write helper, no locking, and no `UpdateHostKeys` /
  `hostkeys@openssh.com` (golang/go#37245, proposal accepted 2024, still unimplemented). Appending
  is ours: `O_APPEND`, 0600, one `Write` per line, plus the leading-newline check OpenSSH does.
- **`@revoked` is global in x/crypto, per-pattern in OpenSSH.** x/crypto keys its revoked set by the
  marshalled key blob alone and ignores the line's host pattern **(probed)**. A `@revoked` line for
  one host revokes that key everywhere.
- **`@cert-authority` works**, contrary to the title of golang/go#33366. But the CA line's host
  pattern carries an implied port 22, so `@cert-authority *.example.com` does not authorise a host
  on `:2222`. It has to be written `[*.example.com]:2222` **(probed)**. Errors on the certificate
  path are plain `errors.errorString`, never `*knownhosts.KeyError`.
- **No Go ssh_config library is faithful, and the common failure is silent.** In
  `kevinburke/ssh_config` v1.6.0 a `Match user …`, `Match final`, `Match exec`, or any criterion
  beyond `all` and `host`, is a whole-file parse error. Under `IgnoreErrors: true` every lookup then
  returns `""` with a nil error **(probed)**. `Match user git` is ordinary in a developer's
  `~/.ssh/config`. No token (`%h`, `%r`, …) is expanded, `~` is not expanded, and `ProxyJump` comes
  back as an unparsed string. See §3.

Repo touchpoint: `platform/vps/provider/vps.go:22-28` already models the destination as
`Target{Alias, Host, Port, User, IdentityFile}`, where a bare string is an ssh_config alias and an
object is the destination spelled out. `kevinburke/ssh_config v1.6.0` is already in the build as an
indirect dependency (`platform/aws/provider/go.mod:123`, `pkg/providerkit/pulumi/go.mod:63`).

## 1. Callback semantics

### What `knownhosts.New` builds

`New(files...)` reads every file into an unexported `hostKeyDB` and returns
`ssh.CertChecker{IsHostAuthority, IsRevoked, HostKeyFallback}.CheckHostKey`
(`ssh/knownhosts/knownhosts.go:417-436`). `CheckHostKey` branches on whether the presented key is
an `*ssh.Certificate`. If it is not, it delegates to `HostKeyFallback`, the plain-key path. If it
is, it runs the CA path and never touches the plain-key code (`ssh/certs.go:340-364`).

The `addr` argument reaching the callback is verbatim the string passed to `ssh.Dial` or
`ssh.NewClientConn`. It is stored as `dialAddress` and handed to `hostKeyCallback` untouched
(`ssh/client.go:71-88`, `ssh/handshake.go:161`, `ssh/handshake.go:841`). `knownhosts` prefers it
over `remote.String()` when non-empty and requires `net.SplitHostPort` to succeed on it
(`knownhosts.go:343-365`).

### The error shapes

```go
type KeyError struct{ Want []KnownKey }     // Error(): "knownhosts: key is unknown" | "knownhosts: key mismatch"
type RevokedError struct{ Revoked KnownKey }
type KnownKey struct{ Key ssh.PublicKey; Filename string; Line int }
```

The doc comment states the intended contract, "If Want is empty, the host is unknown. If Want is
non-empty, there was a mismatch, which can signify a MITM attack" (`knownhosts.go:317-323`), and
`checkAddr` implements it by appending *every* line whose pattern matches the address into `Want`,
regardless of key type, returning `nil` on the first byte-equal key (`knownhosts.go:370-389`).

The contract is weaker than it reads. `Want` non-empty means only "we have at least one key for
this address and none of them is the one presented". Four different situations produce it:

| Situation | `Want` | `Error()` | Actually |
| --- | --- | --- | --- |
| No line matches the address | empty | key is unknown | TOFU-eligible |
| Server offered a type we have no entry for | non-empty, all of *other* types | key mismatch | benign; OpenSSH would never have negotiated this |
| Server's key for a type we do have differs | non-empty, includes that type | key mismatch | the real alarm |
| Only an `@cert-authority` line matches and the server offers a plain key | non-empty, holding the CA key | key mismatch | first contact |

Probed, with `known_hosts` holding ed25519 and rsa for `multi.example.com`:

```
multi-key host, key 1                    OK
multi-key host, key 2                    OK
multi-key host, key 3 (ecdsa, unknown)   *KeyError len(Want)=2 knownhosts: key mismatch want=[ssh-ed25519(line 8) ssh-rsa(line 9)]
```

So telling TOFU from MITM needs `Want`'s key *types*, not its length. Compare `Want[i].Key.Type()`
against the presented `key.Type()`. If no entry shares the presented type, this is the §5
negotiation failure rather than an attack, and the right response is to retry with
`HostKeyAlgorithms` set from `Want`, not to prompt the user. Getting that list before the first
connection is impossible through the upstream API, which is §5's whole problem.

### Multiple keys per host

Legal and handled. sshd(8): "It is permissible (but not recommended) to have several lines or
different host keys for the same names… authentication is accepted if valid information can be
found from either file", https://man.openbsd.org/sshd#SSH_KNOWN_HOSTS_FILE_FORMAT. x/crypto scans
all lines and accepts on any byte-equal match **(probed above)**. Comma-separated pattern lists,
`*` and `?` wildcards, and `!` negation all work (`knownhosts.go:263-300`, `:78-107`):

```
comma list, first pattern                OK
comma list, wildcard pattern             OK
wildcard is separator-blind              OK      # *.wild.example.com matches a.b.wild.example.com
negated host                             *KeyError len(Want)=0 knownhosts: key is unknown
non-negated host                         OK
```

The wildcard ignores separators on purpose, matching OpenSSH's `addrmatch.c`
(`knownhosts.go:75-77`). **Negation is a trap for TOFU.** `!bad.example.com` yields
`len(Want)==0`, indistinguishable from a host we have never seen, so a naive TOFU flow would
happily append a key for the one host the user wrote a line to exclude.

Matching is case-sensitive in x/crypto (`pat[0] == str[0]`, `knownhosts.go:100`). OpenSSH
lowercases the hostname before both lookup and storage. `sshconnect.c` `ssh_login` does
`host = xstrdup(orighost); lowercase(host);`, and `hostfile.c` `format_host_entry` does
`lhost = xstrdup(host); lowercase(lhost);`
(https://raw.githubusercontent.com/openssh/openssh-portable/master/hostfile.c). **Lowercase the
host ourselves before dialling and before writing**, or a mixed-case alias will miss its own entry.

### Hashed entries

The format is `|1|<base64 salt>|<base64 HMAC-SHA1(salt, host)>`. `HASH_MAGIC "|1|"` and
`HASH_DELIM '|'` are in
https://raw.githubusercontent.com/openssh/openssh-portable/master/hostfile.h, and HMAC-SHA1 is in
`host_hash()` in hostfile.c. sshd(8) documents the constraint that matters: "Only one hashed
hostname may appear on a single line and none of the above negation or wildcard operators may be
applied." A hashed entry can be tested against a candidate name, never enumerated.

x/crypto reads them (`newHashedHost`, `knownhosts.go:528-546`) and rejects any type but `1`.

**The writing gotcha:** `hashedHost.match` hashes `Normalize(a.String())`, but `HashHostname` does
not normalize its input and says so, "The hostname is not normalized before hashing.
// TODO(hanwen): check if we can safely normalize this always" (`knownhosts.go:470-483`).
Probed: a line built from `HashHostname("example.com:2222")` never matches, while one built from
`HashHostname(Normalize("example.com:2222"))`, hashing the string `[example.com]:2222`, matches.
**Always feed `HashHostname` the output of `Normalize`.**

### `@revoked`

x/crypto stores revoked keys in `db.revoked`, a map keyed only by the marshalled key blob, and
`check` consults it before any address matching (`knownhosts.go:230-238`, `:344-346`). The host
pattern on a `@revoked` line is parsed and thrown away. Probed:

```
revoked key at its own host              *RevokedError knownhosts: key is revoked
revoked key at another host              *RevokedError knownhosts: key is revoked
```

That is stricter than sshd(8), which describes revocation per matching line, but the direction is
safe. `RevokedError` is a distinct type from `KeyError`, so a TOFU branch that only type-asserts
`*KeyError` falls through to a generic error and loses the "this key is revoked" wording.

`CertChecker.IsRevoked` is a separate mechanism on the certificate path. It checks both the cert's
own blob and its `SignatureKey` (`knownhosts.go:162-170`).

### `@cert-authority`

Works, despite golang/go#33366's title. Probed against `@cert-authority *.example.com <CA key>`:

```
host cert, port 22                                OK
host cert, port 2222, unported CA pattern         ssh: no authorities for hostname: srv.example.com:2222
host cert, port 2222, CA pattern [*.example.com]:2222   OK
host cert, principal mismatch                     ssh: principal "srv.example.com" not in the set of valid principals ...
host cert, principal spelled "srv.example.com:2222"     ssh: no authorities for hostname: srv.example.com:2222
hashed @cert-authority line                       OK
plain host key under a CA-only entry              *knownhosts.KeyError knownhosts: key mismatch
CA key itself presented as a plain host key       OK
```

Four facts follow:

1. **The CA pattern carries an implied port.** `newHostnameMatcher` defaults a portless pattern to
   `"22"` (`knownhosts.go:287-293`) and `IsHostAuthority` matches host *and* port
   (`knownhosts.go:109-111`, `:146-159`). This is golang/go#52056 in the CA path.
2. **The principal is checked without the port.** `CheckHostKey` splits the address and passes only
   the hostname to `CheckCert` (`ssh/certs.go:340-364`), so `ValidPrincipals` must hold
   `srv.example.com`, never `srv.example.com:2222`.
3. **Cert-path errors are plain `errors.errorString`**, never `*KeyError` or `*RevokedError`. A
   caller's TOFU branch must not treat an unmatched-CA error as an unknown host.
4. **`checkAddr` does not filter on `l.cert`** (`knownhosts.go:377-386`), so a CA line's key both
   pollutes `Want` for hosts under its pattern and is accepted outright if a server presents it as
   a plain host key.

## 2. Appending a TOFU-accepted key

### What the package gives us

`Normalize(address) string` splits host and port (defaulting to 22), strips IPv6 brackets, then
returns the bare host for port 22 and `[host]:port` otherwise (`knownhosts.go:441-458`). Probed:

| input | output |
| --- | --- |
| `example.com:2222` | `[example.com]:2222` |
| `example.com:22` | `example.com` |
| `[2001:db8::1]:2222` | `[2001:db8::1]:2222` |
| `[2001:db8::1]:22` | `2001:db8::1` |

`Line(addresses []string, key) string` normalizes each address, joins with `,`, and appends
`key.Type() + " " + base64(key.Marshal())` (`knownhosts.go:461-468`). Probed:

```
Line([]string{"example.com:2222"}, edA)                    = "[example.com]:2222 ssh-ed25519 AAAA…"
Line([]string{"example.com:2222","203.0.113.5:2222"}, edA) = "[example.com]:2222,[203.0.113.5]:2222 ssh-ed25519 AAAA…"
```

**No trailing newline, no hashing, and no writer.** The godoc lists exactly `HashHostname`, `Line`,
`New`, `Normalize`, `KeyError`, `KnownKey`, `RevokedError`. Hashed writing is possible but manual:
`HashHostname(Normalize(addr)) + " " + key.Type() + " " + base64(key.Marshal())`.

### What OpenSSH does that we have to copy

`add_host_to_hostfile` in https://raw.githubusercontent.com/openssh/openssh-portable/master/hostfile.c:

```c
hostfile_create_user_ssh_dir(filename, 0);
if ((f = fopen(filename, "a+")) == NULL)
        return 0;
setvbuf(f, NULL, _IONBF, 0);
/* Make sure we have a terminating newline. */
if (fseek(f, -1L, SEEK_END) == 0 && fgetc(f) != '\n')
        addnl = 1;
if (fseek(f, 0L, SEEK_END) != 0 || (addnl && fputc('\n', f) != '\n')) { … }
success = write_host_entry(f, host, NULL, key, store_hash);
```

Four behaviours to reproduce: create `~/.ssh` if absent, open in append mode, **add a missing
terminating newline before writing**, and write the whole entry in one unbuffered write
(`setvbuf(_IONBF)` plus a single `fwrite` of the assembled buffer in `write_host_entry`).
`format_host_entry` lowercases the host, hashes it when `store_hash` is set, and terminates with
`\n`. Whether it hashes is `options.hash_known_hosts`, that is `HashKnownHosts`, whose **default is
`no`** (https://man.openbsd.org/ssh_config).

`hostfile_replace_entries`, the rewrite path used for `UpdateHostKeys` and `ssh-keygen -R`, uses
`umask(077)` plus `mkstemp` plus `rename(temp, filename)` with a `.old` backup. A pure-append TOFU
flow does not need it.

### Locking and atomicity

**There is no locking anywhere.** Neither `flock` nor `fcntl` appears in hostfile.c, and x/crypto
has no writer at all. What makes concurrent appends safe is POSIX: with `O_APPEND`, "the file
offset shall be set to the end of the file prior to each write and no intervening file modification
operation shall occur between changing the file offset and the write operation", and "each write is
atomic", https://pubs.opengroup.org/onlinepubs/9699919799/functions/write.html. The `PIPE_BUF`
interleaving guarantee is for pipes and FIFOs only and does not apply here.

The practice that follows:

- `os.MkdirAll(~/.ssh, 0700)`, then
  `os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)`.
- Check the last byte and prepend `\n` when the file does not end in one. That read is not atomic
  against a concurrent appender, so keep it and the write in one buffer where possible, or accept
  the rare blank line.
- **One `Write` call carrying the complete line plus its `\n`.** Never two.
- Never rewrite the file in place for a TOFU accept. Rewriting is the operation that needs
  temp-plus-rename, and we have no reason to do it.
- Windows gives no POSIX guarantee here. If we ever ship a Windows `bootstrap`, the append is
  best-effort and should be described that way.

### Which address to write

OpenSSH writes the key under the `HostName` after alias resolution, lowercased, port-bracketed by
`put_host_port`. It writes a second entry under the IP only when `CheckHostIP` is on, whose
**default is `no`** (https://man.openbsd.org/ssh_config; `get_hostfile_hostname_ipaddr` and the
`HOST_NEW` branch of `check_host_key` in
https://raw.githubusercontent.com/openssh/openssh-portable/master/sshconnect.c). `HostKeyAlias`,
when set, replaces the lookup key entirely. To interoperate with the user's existing file we should
do the same: **key on the resolved `HostName[:Port]`, lowercased, not on the alias**, and write one
entry.

For the TTY prompt, `ssh.FingerprintSHA256(key)` returns `"SHA256:"` plus the unpadded-base64
SHA-256 of the marshalled key (`ssh/keys.go:1928-1932`). It is byte-identical to what
`ssh-keygen -lf` prints, so the user can cross-check.

## 3. ssh_config and alias resolution

`x/crypto/ssh` does not parse `ssh_config` at all. Its API is protocol-level
(https://pkg.go.dev/golang.org/x/crypto/ssh). `kevinburke/ssh_config`'s README positions itself as
the complement: "It's designed to be used with the excellent x/crypto/ssh package, which handles
SSH negotiation but isn't very easy to configure."

### `kevinburke/ssh_config` v1.6.0

Latest release is v1.6.0 (2026-02-16). `master` carries an unreleased 1.7 whose defaults table
differs (https://proxy.golang.org/github.com/kevinburke/ssh_config/@v/list,
https://raw.githubusercontent.com/kevinburke/ssh_config/master/CHANGELOG.md). We already have
v1.6.0 in the build indirectly.

Two entry points, with different semantics that the godoc does not make obvious:

```go
func Decode(r io.Reader) (*Config, error)          // parse one file
func (c *Config) Get(alias, key string) (string, error)
func (c *Config) GetAll(alias, key string) ([]string, error)

type UserSettings struct{ IgnoreErrors bool }      // the ~/.ssh/config + /etc/ssh/ssh_config stack
func (u *UserSettings) ConfigFinder(f func() string)
func (u *UserSettings) Get(alias, key string) string
func (u *UserSettings) GetStrict(alias, key string) (string, error)
func (u *UserSettings) GetAll(alias, key string) []string
func (u *UserSettings) GetAllStrict(alias, key string) ([]string, error)

func Default(keyword string) string
```

**`Config.Get` applies neither defaults nor validation. `UserSettings.Get` applies both** (probed):

```
Config.Get(Port) with "Port notanumber"          = "notanumber"  err=<nil>
Config.Get(StrictHostKeyChecking) absent         = ""            err=<nil>
UserSettings.Get(Port) with "Port notanumber"    = ""
UserSettings.GetStrict(Port)                     = ""  err=ssh_config: strconv.ParseUint: parsing "notanumber": invalid syntax
UserSettings.Get(StrictHostKeyChecking) absent   = "ask"
```

`UserSettings` reads `$HOME/.ssh/config` then `/etc/ssh/ssh_config`, falling back to `Default(key)`.
`ConfigFinder` replaces both with a single file. `Default` is a lowercase-keyed `map[string]string`
covering 63 keys in v1.6.0, including `StrictHostKeyChecking` → `"ask"`, `UserKnownHostsFile` →
`"~/.ssh/known_hosts ~/.ssh/known_hosts2"`, `HashKnownHosts` → `"no"`, `Port` → `"22"`, and
`IdentityFile` → `"~/.ssh/identity"`. `User` has no default. The 1.7 branch drops the
`IdentityFile` default, and its replacement `defaultIdentityFiles` is declared but unreferenced, so
`Default("IdentityFile")` will start returning `""`.

### What it honours

| | v1.6.0 behaviour |
| --- | --- |
| `HostName`, `Port`, `User`, `IdentityFile`, `ProxyJump`, `ProxyCommand` | Returned as opaque strings. Zero semantic knowledge of any of them: no `ssh.ClientConfig` construction, no ProxyJump chain parsing, no `HostName` feeding back into matching. (Open feature request: kevinburke/ssh_config#90) |
| Keys | Case-insensitive, values case-preserved. Probed: `hostname` and `port` resolve. |
| `Host` patterns | `*` and `?` wildcards, comma lists, `!` negation. Probed: negation makes the whole line not match. |
| First-value-wins | Correct. File order, then line order, then user file before system file. Probed: `Host b*` before `Host box` wins. |
| Multiple `IdentityFile` lines | `GetAll` returns all of them, `Get` returns the first. But `SupportsMultiple` is broken in v1.6.0: it does `pluralDirectives[strings.ToLower(key)]` against a map whose keys are `"CertificateFile"`, `"IdentityFile"`, `"DynamicForward"`, `"RemoteForward"`, `"SendEnv"`, `"SetEnv"`. Mixed case, so every lookup misses and it always returns `false` (`validators.go:174-186` in the module cache; probed for both casings). Do not branch on it. |
| `Include` | Implemented, eager, `filepath.Glob`-ed, depth-capped at 5 (`ErrDepthExceeded`), works inside a `Host` block. Probed: an absolute `Include` resolves. Relative paths join `$HOME/.ssh`, or `/etc/ssh` in system mode; `~/` handled since v1.6.0. Directives are split on a plain space, so a quoted include path containing spaces mis-splits. |
| `Match` | `all` and `host` only. Everything else is a hard parse error. |
| Tokens (`%h`, `%p`, `%r`, `%d`, `%u`, `%L`, `%l`, `%n`, `%C`, …) | Not expanded at all. No expansion function exists in the package. Open since the project's start as kevinburke/ssh_config#2. Probed: `HostName %h.internal` comes back verbatim. |
| `~` in values | Not expanded. Probed: `IdentityFile ~/.ssh/id_box` comes back as `"~/.ssh/id_box"`. Only `Include` paths get `~` treatment. |
| `${VAR}` in values | Not expanded. |

### The `Match` cliff

Probed against v1.6.0:

```
Match all present         HostName="10.0.0.5" Port="2222"
Match host box present    HostName="10.0.0.5" Compression="yes"
Match user git present    Decode error: (7, 7): ssh_config: unsupported Match criterion "user"
Match exec "true"         Decode error: (7, 7): ssh_config: Match Exec is not supported
Match final present       Decode error: (7, 7): ssh_config: unsupported Match criterion "final"
```

The error is not scoped to the block. It fails the whole decode, so every lookup against that file
is lost:

```
file containing `Match user git`:
  Get(HostName)       = ""
  GetStrict(HostName) = "" err=(3, 7): ssh_config: unsupported Match criterion "user"
  IgnoreErrors:true   = "" err=<nil>          <-- silently empty
```

`IgnoreErrors: true` is the dangerous setting. It turns a config we cannot read into a config that
looks empty. A user whose `~/.ssh/config` has the very common

```
Match user git
  IdentitiesOnly yes
```

would find `ocel bootstrap production` unable to resolve any alias, and with `IgnoreErrors`, with
no explanation. **If we take this dependency, use `GetStrict` and `GetAllStrict`, never
`IgnoreErrors`, and report the parse error with the file and line the library already gives us.**

`Match exec` being refused is deliberate. The source comment cites arbitrary code execution from an
untrusted config, and we should keep that refusal rather than reach for a library that runs it.

The README is stale on this point ("Notably, the `Match` directive is currently unsupported") and
the v1.5 CHANGELOG overstates it ("Most of the Match spec is implemented, including `Match host`,
`Match originalhost`, `Match user`, `Match localuser`, and `Match all`"). The parser is the truth.

### Alternatives

| Library | What it is | `Include` | `Match` | Tokens |
| --- | --- | --- | --- | --- |
| `kevinburke/ssh_config` v1.6.0 | Comment-preserving parser plus `Get(alias,key)` | yes, depth 5 | `all`, `host`; everything else is a whole-file error | no |
| `trzsz/ssh_config` | Fork; `shlex`-splits Include directives | yes, quoted paths work | `all`, `host`; other criteria silently skipped to the next block | no |
| `shuLhan/share/lib/ssh/config` | Semantic parser with typed accessors (`Hostname()`, `Port()`, `User()`, `Signers()`) | yes, inlined before section parsing | criteria model for `host`, `user`, `localuser`, `originalhost` with negation; `canonical`, `exec`, `final` are `//TODO` stubs returning false | no |
| `mikkeloscar/sshconfig` | Parser into `[]*SSHHost` | yes | no | no |
| `melbahja/goph` | Client wrapper; never reads `~/.ssh/config` (no ssh_config dep in go.mod) | n/a | n/a | n/a |
| `gliderlabs/ssh` | SSH server library | n/a | n/a | n/a |

`shuLhan/share` is the only one with a real criteria model, but its `Section.merge` assigns in
order so the **last** matching block wins, inverted from OpenSSH's documented first-value-wins
(https://raw.githubusercontent.com/shuLhan/share/main/lib/ssh/config/section.go). A pkg.go.dev
search for `ssh_config` returns mostly forks and vendored copies of kevinburke's. **There is no
independent, spec-complete Go implementation.**

For reference, `go-git` pins `kevinburke/ssh_config v1.2.0` and `skeema/knownhosts v1.3.0`
(https://raw.githubusercontent.com/go-git/go-git/master/go.mod), and v1.2.0 predates any `Match`
support at all. `buildkit` has neither dependency and does not parse ssh_config.

### What OpenSSH specifies, and what we would be giving up

From https://man.openbsd.org/ssh_config:

- Precedence: command-line options, then `~/.ssh/config`, then `/etc/ssh/ssh_config`. "Unless noted
  otherwise, for each configuration directive, the first specified value will be used… more
  host-specific declarations should be given near the beginning of the file, and general defaults
  at the end." Keywords are case-insensitive, arguments case-sensitive, separated by whitespace or
  "exactly one '=' character".
- `Include`: glob wildcards, tokens, `${VAR}` environment variables, and `~` references.
  "Wildcards will be expanded and processed in lexical order." Relative paths join `~/.ssh` for a
  user config or `/etc/ssh` for the system one, and "Include directive may appear inside a `Match`
  or `Host` block to perform conditional inclusion."
- `Match` criteria: `canonical`, `final`, `exec`, `localnetwork`, `host`, `originalhost`, `tagged`,
  `command`, `user`, `localuser`, `version`, plus the bare `all`. Criteria may be negated with `!`,
  and non-`all` criteria take comma-separated pattern-lists.
- Tokens accepted by `IdentityFile`, `Include`, `UserKnownHostsFile`, `CertificateFile`,
  `ControlPath`, `IdentityAgent`, `KnownHostsCommand`, `RemoteCommand` and others: `%%`, `%C`,
  `%d`, `%h`, `%i`, `%j`, `%k`, `%L`, `%l`, `%n`, `%p`, `%r`, `%u`. `Hostname` accepts only `%%` and
  `%h`. `ProxyCommand` and `ProxyJump` accept `%%`, `%h`, `%n`, `%p`, `%r`.
- `ProxyJump`: "one or more jump proxies as either `[user@]host[:port]` or an ssh URI. Multiple
  proxies may be separated by comma characters and will be visited sequentially… Setting the host
  to `none` disables this option entirely." And, importantly, "the configuration for the
  destination host… is not generally applied to jump hosts", so each hop resolves as its own alias.

Given `Target{Alias, Host, Port, User, IdentityFile}` in `platform/vps/provider/vps.go:22-28`, the
practical shortfall is small and bounded. Resolve `HostName`, `Port`, `User` and `IdentityFile` for
one alias, then **expand `~` and any `%h`, `%r`, `%d`, `%n`, `%p`, `%u` ourselves**, because the
library does neither. `ProxyJump` would need its own `[user@]host[:port]` parse plus a recursive
resolve of each hop as an alias, and would arrive with no tokens expanded either.

## 4. ssh-agent

`agent.NewClient(rw io.ReadWriter) ExtendedAgent` takes any connection. The package does **not**
read `SSH_AUTH_SOCK` for you. The package example is the whole flow
(`ssh/agent/example_test.go`):

```go
socket := os.Getenv("SSH_AUTH_SOCK")
conn, err := net.Dial("unix", socket)
agentClient := agent.NewClient(conn)
config := &ssh.ClientConfig{
        Auth: []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient.Signers)},
        …
}
```

The example's own comment gives the reason to prefer the callback form: "Use a callback rather than
PublicKeys so we only consult the agent once the remote server wants it."

- `Signers() ([]ssh.Signer, error)` returns one `agentKeyringSigner` per agent identity
  (`ssh/agent/client.go:818-830`). Each implements `ssh.AlgorithmSigner`. `SignWithAlgorithm` maps
  `rsa-sha2-256` and `rsa-sha2-512` onto the agent's `SignatureFlagRsaSha256` and `RsaSha512` and
  errors on anything else (`client.go:846-862`), so RSA keys in an agent do negotiate SHA-2
  correctly.
- Since x/crypto v0.54.0, `NewClient` pipelines concurrent requests when `rw` implements
  `io.Closer`, which `*net.UnixConn` does, and falls back to full serialization otherwise
  (`client.go:334-356`).
- Agent forwarding is `agent.RequestAgentForwarding(session)` plus `agent.ForwardToAgent` or
  `ForwardToRemote`. Bootstrap should not need it.

### Combining an agent with an explicit IdentityFile

x/crypto has no OpenSSH-equivalent policy here: no `IdentitiesOnly`, no ordering rule. Both sources
are just `ssh.Signer`s, and `ssh.PublicKeys(signers...)` and `ssh.PublicKeysCallback(fn)` take them
in the order given. The client walks the list, sending a publickey *query* per signer before
signing with the first the server accepts (`ssh/client_auth.go:334-380`).

**That query counts against the server's `MaxAuthTries`, whose default is 6**
(https://man.openbsd.org/sshd_config). A developer with eight keys loaded in their agent and an
explicit `identityFile` gets disconnected before the right key is reached. The mitigation is
OpenSSH's: **when the target names an `identityFile`, use that signer alone**, which is the
`IdentitiesOnly yes` behaviour, and fall back to the agent's full list only when it does not.

### Encrypted keys

`ssh.ParsePrivateKey(pem)` returns `*ssh.PassphraseMissingError` when the key is encrypted
(`ssh/keys.go:1310-1320`, `:1342-1352`). The type carries the public half when the format includes
it unencrypted, which the OpenSSH `-----BEGIN OPENSSH PRIVATE KEY-----` format does, so we can show
the user which key we are asking about before prompting. `ParsePrivateKeyWithPassphrase` takes the
passphrase. There is no agent-style caching and no `ssh-askpass` integration, so prompting is
entirely ours (`internal/prompt` on the CLI side). This is another argument for preferring the
agent: an agent-held key needs no passphrase at all.

## 5. Host-key algorithm pitfalls

### The failure

`ClientConfig.HostKeyAlgorithms` "lists the public key algorithms that the client will accept from
the server for host key authentication, in order of preference. If empty, a reasonable default is
used" (`ssh/client.go:314-319`). The default is `defaultHostKeyAlgos` (`ssh/common.go:177-195`),
which orders RSA and ECDSA certificate algorithms first, then ECDSA plain keys, then RSA, and
**ed25519 last**. OpenSSH's documented default is the opposite for plain keys:
`ssh-ed25519-cert-v01@openssh.com`, … then `ssh-ed25519`, then the ECDSA curves, then
`rsa-sha2-512` and `rsa-sha2-256` (https://man.openbsd.org/ssh_config, HostKeyAlgorithms).

So against a stock sshd serving ed25519, ecdsa and rsa host keys, x/crypto negotiates
**ecdsa-sha2-nistp256** where OpenSSH negotiates **ssh-ed25519**. A `known_hosts` written by `ssh`
therefore holds an ed25519 line, and the Go client, receiving an ecdsa key it has no entry for,
gets `Want` non-empty and reports "knownhosts: key mismatch". Nothing is wrong. The two clients
asked for different keys.

### What OpenSSH does that x/crypto does not

One sentence in ssh_config(5) under HostKeyAlgorithms is the whole difference:

> If hostkeys are known for the destination host then this default is modified to prefer their
> algorithms.

x/crypto has no equivalent, and `knownhosts` exports nothing that would let a caller build one. Its
entire API is `HashHostname`, `Line`, `New`, `Normalize` and the three error and key types.
`hostKeyDB` and its `lines` are unexported.

This is **golang/go#29286, "x/crypto/ssh: client requires first hostkey to match, knownhosts
doesn't expose available key types"**, open since 2018-12-15, labelled `NeedsFix`. Filippo
Valsorda, 2022-03-14: "It does look like there should be an easy way to set or better yet sort
ClientConfig.HostKeyAlgorithms based on the available known keys (which is how OpenSSH handles
this), but it's not immediately clear what it would be… At the very least, we should document the
need to match them." An API for it is proposed in golang/go#68619 (open, `Proposal-Crypto`), and a
CL adding reusable known_hosts database APIs,
https://go-review.googlesource.com/c/crypto/+/800680, is open and unmerged as of 2026-07.

`UpdateHostKeys` is the second, independent gap. It is on by default in OpenSSH, "UpdateHostKeys is
enabled by default if the user has not overridden the default UserKnownHostsFile setting and has
not enabled VerifyHostKeyDNS", and it lets a server advertise its full key set after authentication
so rotation is graceful. x/crypto implements neither `hostkeys-00@openssh.com` nor
`hostkeys-prove-00@openssh.com`; a case-insensitive grep of `golang/crypto` master finds zero
matches, and the only `hostkeys` substring in `ssh/` is the `knownhosts.KeyError` doc comment.
**golang/go#37245** has been `Proposal-Accepted` since 2024-04 with an open, year-stale CL at
https://go-review.googlesource.com/c/crypto/+/559055. The consequence for us: a Go client cannot
learn a rotated host key the way `ssh` does, so a genuine rotation reaches the user as a mismatch.

### Mitigations, in the order to consider them

1. **Set `HostKeyAlgorithms` from the known_hosts entries for the target before dialling.** This is
   what OpenSSH does, and it removes the failure rather than papering over it. Upstream gives no
   way to read the entries, so it means either `skeema/knownhosts` or our own parser.
2. **`github.com/skeema/knownhosts`** (Apache-2.0, wraps `golang.org/x/crypto v0.52.0`, README at
   https://github.com/skeema/knownhosts). `NewDB(files...) (*HostKeyDB, error)` gives
   `HostKeys(hostWithPort) []PublicKey`, each flagged CA or not, and
   `HostKeyAlgorithms(hostWithPort) []string`. `IsHostKeyChanged(err)` and `IsHostUnknown(err)` are
   the `errors.As` helpers over `*xknownhosts.KeyError` we would otherwise write. It recovers the
   key list through the same trick we would: call the upstream callback with a bogus key and read
   `KeyError.Want`. It reads the file a second time to detect `@cert-authority` lines so it can
   return cert algorithms. The older `New` constructor does not, and its own README says that path
   will "(incorrectly) look like normal non-CA host keys" and "lacks the fix for applying `*`
   wildcard known_host entries to all ports". `WriteKnownHost(w, hostname, remote, key)` and
   `WriteKnownHostCA(w, hostPattern, key)` exist, and **neither hashes**; both emit cleartext, the
   equivalent of `HashKnownHosts no`. It does not handle `ssh.FixedHostKey`.
3. **Parse known_hosts ourselves with `ssh.ParseKnownHosts`** (`ssh/keys.go:142`), which returns
   `(marker, hosts, pubKey, comment, rest, err)` per entry. That buys full control over algorithm
   ordering, the case-folding fix, hashed writing and per-pattern `@revoked` semantics, at the cost
   of reimplementing pattern matching, hashed-entry probing and negation. Worth weighing against
   (2) only if we want hashed writing or per-pattern revocation, since those are the two things
   skeema does not give us.
4. **Do not** simply pin `HostKeyAlgorithms` to a fixed list. It swaps a spurious mismatch for a
   `*ssh.AlgorithmNegotiationError` ("ssh: no common algorithm for host key…",
   `ssh/common.go:411-424`) against any host that does not serve the pinned type.

### Adjacent facts worth knowing

- `ssh.SupportedAlgorithms()` and `ssh.InsecureAlgorithms()` return the package's algorithm lists
  including `HostKeys` (`ssh/common.go:294-317`). Useful for an intersection, useless for ordering
  by what we know.
- `ssh.NewControlClientConn(c net.Conn)` (v0.54.0) speaks the client half of OpenSSH's
  ControlMaster multiplexing protocol over an existing control socket. Its own doc warns that
  "proxy mode bypasses the standard cryptographic handshake", so the connection must already be
  local and trusted. It matters only if we ever want to ride a user's existing `ControlPersist`
  session, and in that case OpenSSH did the host-key check, not us.
- ProxyJump has no built-in support. The shape is `ssh.Dial` to the jump host, then
  `client.Dial("tcp", target)` for a `net.Conn`, then `ssh.NewClientConn(conn, target, cfg)`. The
  inner `NewClientConn` gets its own `HostKeyCallback`, so both hops are checked.

## What #564 has to decide

1. **Who reads known_hosts.** Upstream `knownhosts` alone cannot do the job. The mismatch/unknown
   split is wrong for the common case, and there is no way to pre-order `HostKeyAlgorithms`. Either
   take `skeema/knownhosts` (Apache-2.0, one dependency, gives `HostKeyAlgorithms` and the CA-aware
   `NewDB`, cannot write hashed entries) or parse with `ssh.ParseKnownHosts` ourselves (full
   control, and we reimplement pattern matching, hashed probing and negation).
2. **Whether the TOFU prompt is reached by `len(Want)` or by key type.** `len(Want) == 0` alone is
   wrong: it misses the negation case and mislabels an algorithm mismatch as MITM. The honest test
   is "no entry for this host at all" *and* "no entry of the presented type", with the
   algorithm-mismatch case retried rather than prompted.
3. **What we write, and hashed or not.** OpenSSH's default is unhashed (`HashKnownHosts no`), and
   skeema's writer cannot hash. Following the default keeps us out of the business and matches what
   the user's own `ssh` would have written. Honouring a user's `HashKnownHosts yes` means writing
   the line by hand, `HashHostname(Normalize(addr))`, and losing skeema's writer.
4. **Whether `@cert-authority` is in scope at all.** It works, but only with the port baked into the
   pattern, and its errors are untyped. Supporting it means a third branch in the trust decision
   that is neither TOFU nor mismatch.
5. **How much ssh_config we resolve.** The bounded version, `HostName`, `Port`, `User` and
   `IdentityFile` for one alias with our own `~` and token expansion, is a day's work on
   `kevinburke/ssh_config` v1.6.0. `ProxyJump`, `Match` beyond `all` and `host`, and `Match exec`
   are each a cliff, and a config the library refuses has to produce a named error, never a silent
   empty resolution.
6. **`IdentitiesOnly` behaviour.** With an agent loaded and an explicit `identityFile`, offering
   both blows `MaxAuthTries 6`. Decide that an explicit `identityFile` means that key alone.
7. **Rotation has no answer in Go.** `UpdateHostKeys` is unimplemented upstream. A host that rotates
   its key reaches the user as a mismatch, and the remedy we print
   (`ssh-keygen -R '[host]:port'`) has to say so.
