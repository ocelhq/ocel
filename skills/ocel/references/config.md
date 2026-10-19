# Project config

`ocel init` writes the config: `ocel.config.ts` in a Node project, `ocel.json` otherwise
(`--format` picks another). Ocel finds it in this order:
1. `--config`
2. `$OCEL_CONFIG`
3. the nearest `ocel.json`, `ocel.yaml`, `ocel.yml` or `ocel.config.ts`

The JSON Schema is at `https://ocel.dev/schema/ocel.schema.json`, and an editor validates against it through `$schema`. Read the schema for any key not covered below.

```json
{
  "$schema": "https://ocel.dev/schema/ocel.schema.json",
  "slug": "shop",
  "provider": "aws",
  "apps": [
    { "name": "web", "path": "apps/web" },
    { "name": "api", "path": "apps/api", "compute": "container" }
  ]
}
```

- **`slug`:** the project's identity, and the only required key. Changing it creates a different project, with new infrastructure.
- **`provider`:** where the apps run: `"aws"` alone, or `{ "<id>": { …options } }` with `aws`, `gcp` or `vps` as the id (`gcp` and `vps` always take this form) for provider options, `edge` (for example Cloudflare in front) and `dns`.
- **`apps`:** each app's `name` and `path`.
  - The framework (`node`, `next`, `sveltekit`, `nestjs`, `go`, `python`, `rust`) is detected only when exactly one framework matches the app's directory, and `ocel doctor` prints each app's framework and the evidence. When none or several match, set the app's own `framework`. Set `entrypoint` where the app starts elsewhere than ocel looks: a node file (for `nestjs`, the compiled one), a go package's directory, a python file or `module:name`, or the binary to run when a rust crate builds several. Detection does not read `entrypoint`, so an app found only through it also sets `framework`. An app no detector matches whose directory holds one language's manifest (a `package.json` with only a `start` script, say) is written in that language; with several languages' manifests, set `framework`.
  - `compute` is `serverless` or `container`, named alone or keyed with its options.
  - Optional per-app keys: `domains.production` and `arch`.
- **`domains`:** the project's production domain and the preview wildcard (`*.preview.example.com`).
- **`protection`:** `{ "production": true, "preview": true }` makes that tier ask for a password before it answers; each tier has a password of its own, and `ocel protection password` shows it and `ocel protection password rotate` rotates it (add `--preview` for previews). The human's call: it locks visitors out.
- **`bindings`:** binds a declared `postgres`, `bucket` or `kv` to something that already exists instead of provisioning it.
- **`discovery.paths`:** where declarations live, when not in the default discovery folder.
- **`envSource`:** where variable values come from per tier: `dotenv` in dev, Ocel's own store when deployed, or an `exec` command or Infisical.
- **`lifecycle.preBuild`:** a command run before each build, such as a migration.

Before the first deploy to an account, its shared infrastructure must exist: `ocel bootstrap production` (and `ocel bootstrap preview` for previews). `bootstrap` changes the user's account, so it is the human's call. `bootstrap.missing` means it hasn't been run. `bootstrap.parts_missing` means the bootstrap lacks a part only the bootstrap credential may add, such as the variables key or a Cloudflare edge's; the refused command recorded that the project wants it, and `ocel bootstrap <tier>` adds it. A `--dry` run records nothing, so its hint says to run the command without `--dry` first.
