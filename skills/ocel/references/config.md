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
- **`provider`:** where the apps run: `aws`, `gcp` or `vps`, or `{ "<id>": { …options } }` for provider options, `edge` (for example Cloudflare in front) and `dns`.
- **`apps`:** each app's `name` and `path`.
  - The framework is detected from the app (`node`, `next`, `sveltekit`, `go`, `python`, `rust`); set `framework` only to override it.
  - `compute` is `serverless` or `container`.
  - Optional per-app keys: `domains.production` and `arch`.
- **`domains`:** the project's production domain and the preview wildcard (`*.preview.example.com`).
- **`bindings`:** binds a declared `postgres`, `bucket` or `kv` to something that already exists instead of provisioning it.
- **`discovery.paths`:** where declarations live, when not in the default discovery folder.
- **`envSource`:** where variable values come from per tier: `dotenv` in dev, Ocel's own store when deployed, or an `exec` command or Infisical.
- **`lifecycle.preBuild`:** a command run before each build, such as a migration.

Before the first deploy to an account, its shared infrastructure must exist: `ocel bootstrap production` (and `ocel bootstrap preview` for previews). `bootstrap` changes the user's account, so it is the human's call. `bootstrap.missing` means it hasn't been run.
