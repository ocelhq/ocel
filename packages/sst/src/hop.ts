import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { join } from "node:path";
import type { LinkRecord } from "./record.js";

/** The ocel coordinate a publisher instance targets, and what it publishes there. */
export interface PublishRequest {
  project: string;
  publisher: string;
  class: "production" | "preview";
  environment?: string;
  region?: string;
  records?: LinkRecord[];
}

/** What the publisher answers with: the names it wrote, and how many rows its prune took. */
export interface PublishResponse {
  published?: string[];
  pruned?: number;
  table?: string;
}

export type Command = "publish-links" | "prune-links";

export type Spawn = (
  command: string,
  args: string[],
  input: string,
) => { status: number | null; stdout: string; stderr: string };

export interface Hop {
  spawn?: Spawn;
  resolve?: () => string;
}

/**
 * Runs one publish or prune through the ocel AWS publisher and returns its answer.
 *
 * The hop is a single process invocation under the credentials this apply
 * already holds — no ocel token crosses it — and the publisher's own refusal is
 * what surfaces when it exits non-zero.
 */
export function hop(
  command: Command,
  request: PublishRequest,
  hooks: Hop = {},
): PublishResponse {
  const resolve = hooks.resolve ?? resolvePublisher;
  const run = hooks.spawn ?? runPublisher;

  let binary: string;
  try {
    binary = resolve();
  } catch (cause) {
    throw new Error(
      `@ocel/sst found no ocel AWS publisher for ${process.platform}-${process.arch}. Install @ocel/sst with its optional platform packages, or add @ocel/provider-aws-${process.platform}-${process.arch} to this config's dependencies.`,
      { cause },
    );
  }

  const result = run(binary, [command], `${JSON.stringify(request)}\n`);
  if (result.status !== 0) {
    throw new Error(refusal(result.stderr, result.status));
  }
  try {
    return JSON.parse(result.stdout) as PublishResponse;
  } catch (cause) {
    throw new Error(
      `could not read what the ocel publisher answered: ${result.stdout.trim() || "nothing"}`,
      { cause },
    );
  }
}

function refusal(stderr: string, status: number | null): string {
  const said = stderr.trim();
  if (!said) {
    return `the ocel publisher exited ${status ?? "on a signal"} without saying why`;
  }
  return said;
}

function resolvePublisher(): string {
  const require = createRequire(import.meta.url);
  const pkg = `@ocel/provider-aws-${process.platform}-${process.arch}`;
  const binary = process.platform === "win32" ? "deploy.exe" : "deploy";
  return require.resolve(join(pkg, "bin", binary));
}

function runPublisher(
  command: string,
  args: string[],
  input: string,
): { status: number | null; stdout: string; stderr: string } {
  const result = spawnSync(command, args, { input, encoding: "utf8" });
  if (result.error) {
    throw result.error;
  }
  return {
    status: result.status,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}
