---
name: ocel
description: Ocel deploys apps into the user's own cloud account (AWS, GCP or a VPS). Use when deploying, previewing or rolling back an app with `ocel`; when declaring or using cloud resources from app code through the `ocel` SDK (Postgres, buckets, topics, tasks and cron, KV, realtime, environment variables); or when reading `ocel.json`, `ocel` command output or an Ocel error code.
---

# Ocel

Ocel builds, provisions and releases apps into the user's own provider account. There is no
separate infrastructure file: a resource exists because app code calls the SDK for it, and
that call is the **declaration**. Ocel finds declarations by running the files under the
project's discovery folder (`infra/` by convention), so every declaration lives there and
app code imports the handle from it.

Everything the user owns sits in their account under their billing. Treat each change to it
as theirs to approve.

## The working loop

1. **Orient.** Run `ocel doctor --json`. It is done when `data` reports every check passing.
   Each failing check names its fix; apply it, or hand it to the human when it needs an
   account, credentials or money. With no project config, the error is `project.no_config`:
   ask the human which provider (`aws`, `gcp` or `vps`) and run
   `ocel init --provider <id>`. When init answers `input_required` for a provider option
   (`--option ssh=<target>` for a VPS, for example), the value names the human's own
   machine or account. Local work never uses it: when the task stays in `ocel dev`, pass a
   placeholder such as `deploy@example.invalid` and tell the human to replace it before
   the first deploy; otherwise ask for it.
2. **Declare.** Add or change resources in the discovery folder. Open the reference for each
   kind you touch (table below) before writing the call. Files in the discovery folder only
   declare: Ocel runs them to discover resources, before any resource exists, so queries,
   uploads and other I/O belong in app code or a one-off `ocel run` script.
3. **Run locally.** `ocel dev -- <the app's own dev command>` (for example
   `ocel dev -- pnpm dev`). It starts every declared resource in Docker and runs the app
   against them. It is done when the app serves requests and the code path you changed has
   run once against its resource. `ocel run -- <cmd>` runs a one-off command (a migration,
   a script) against the same resources.
4. **Plan.** `ocel deploy --dry --json` for production, `ocel preview up --dry --json` for a
   preview. The **plan** in the run events is every change the deploy would make to the
   account. Summarise it for the human: what is created, replaced or deleted.
5. **Release.** A preview the human asked for is yours to run: `ocel preview up --json`.
   Production waits for the human to approve the plan you showed; then `ocel deploy --json`.
6. **Verify.** The run's final `summary` carries the URLs. `ocel logs --since 15m` reads
   what the deployed app logged; add `--preview` for a preview.

## Reading output

Pass `--json` (or set `OCEL_JSON=1` once) on every command you run.

- A command that returns data prints one line: `{"ok":true,"data":{…}}` or
  `{"ok":false,"error":{…}}`.
- A command that changes something (`deploy`, `preview up`, `build`, `rollback`,
  `bootstrap`, `destroy`) prints one JSON event per line and ends with a `summary` event
  holding `success`, the apps and URLs, `error` on failure, and `assumed`.
- `ocel schema <command>` prints the JSON Schema of a command's output.

An `error` has a stable `code`, a `message`, often a `hint` and a `docs_url`. Act on the
`hint` first: it is the exact command or flag that resolves the error. When the hint is not
enough, fetch the `docs_url` with `.md` appended for that error's page as markdown.
`input_required` names the flag that supplies the missing answer; supply it, or ask the
human for the value when it is theirs to choose. `assumed` lists confirmations Ocel took on
the human's behalf because nobody could be asked (such as creating a new project); report
each one to the human.

## Finding a command or flag

Ask the binary, which always matches the installed version:

- `ocel help --json` — every command, argument and flag, with whether it mutates.
- `ocel help <command> --json` — one command.
- `https://ocel.dev/llms.txt` — the docs index; any docs page is markdown at its URL plus `.md`.

## The human's decisions

These stay with the human; bring them the plan or the question and wait:

- production deploys, rollbacks, `bootstrap` and every `destroy`;
- `--yes` on any of those, which is consent to a plan the human has read;
- choosing the provider, the account, a domain, or anything that costs money.

When a `hint` offers a way to confirm on the human's behalf, pass the decision to the human.
Secret values stay masked: `ocel env ls` and `ocel env get` show what is set; reveal a value
only when the human asks to see it.

## Words

Use these as Ocel means them when talking to the human:

- **plan** — the diff a human consents to before a change.
- **deployment** — one whole-project deploy, as `ocel promotions ls` lists them.
- **release** — one app's build made live in a deployment.
- **preview** — a deployment of a branch, beside production.
- **origin** — the cloud an app runs in; **edge** — what serves requests in front of it.

## References

Open the one you need; each covers TypeScript, Go, Python and Rust.

| Working on | Read |
| --- | --- |
| a Postgres database | [references/postgres.md](references/postgres.md) |
| files and uploads | [references/bucket.md](references/bucket.md) |
| background jobs, retries, cron | [references/task.md](references/task.md) |
| messages fanned out to consumers | [references/topic.md](references/topic.md) |
| a cache, counters, sessions | [references/kv.md](references/kv.md) |
| live updates to browsers | [references/realtime.md](references/realtime.md) |
| environment variables and secrets | [references/variables.md](references/variables.md) |
| `ocel.json`: apps, providers, domains | [references/config.md](references/config.md) |
