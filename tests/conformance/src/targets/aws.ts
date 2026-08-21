import { execFileSync } from "node:child_process";
import { readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { nextDotenv } from "../env";
import { repoRoot } from "../examples";
import { successful } from "../process";
import type { Example, Target } from "../types";

const deployResult = path.join(".ocel", "deploy-result.json");
const config = "ocel.aws.config.ts";
const skipDriftChecks = {
  OCEL_SKIP_EDGE_RECONCILE: "1",
  OCEL_SKIP_TEARDOWN_REFRESH: "1",
};

type Result = {
  slug: string;
  environment: { class: string; identity?: string };
  appUrls: string[];
};

function ocelCommand() {
  if (process.env.OCEL_BIN) return [process.env.OCEL_BIN, []] as const;
  return [
    process.execPath,
    [path.join(repoRoot, "packages", "ocel", "bin", "run.js")],
  ] as const;
}

function childEnv(token: string, slug: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_CONFIG: config,
    OCEL_EDGE_OBSERVABILITY: "off",
    OCEL_TEST_PROJECT_SLUG: slug,
    ...skipDriftChecks,
  };
}

async function runOcel(
  label: string,
  args: string[],
  example: Example,
  env: NodeJS.ProcessEnv,
) {
  const [command, prefix] = ocelCommand();
  return successful(label, command, [...prefix, ...args], {
    cwd: example.dir,
    env,
    timeoutMs: 25 * 60_000,
  });
}

function aws(args: string[]) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: 30_000,
    maxBuffer: 16 * 1024 * 1024,
    env: { ...process.env, AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  }).trim();
}

function functionName(result: Result, example: Example) {
  const environment =
    result.environment.class === "preview"
      ? `preview-${result.environment.identity ?? ""}`
      : "prod";
  const response = JSON.parse(
    aws([
      "resourcegroupstaggingapi",
      "get-resources",
      "--tag-filters",
      `Key=ocel:project,Values=${result.slug}`,
      `Key=ocel:app,Values=${example.appName}`,
      `Key=ocel:env,Values=${environment}`,
      "--resource-type-filters",
      "lambda:function",
      "--output",
      "json",
    ]),
  ) as { ResourceTagMappingList?: Array<{ ResourceARN?: string }> };
  const names = (response.ResourceTagMappingList ?? []).flatMap((entry) => {
    const match = /^arn:aws:lambda:[^:]*:[^:]*:function:([^:]+)/.exec(
      entry.ResourceARN ?? "",
    );
    return match?.[1] ? [match[1]] : [];
  });
  if (names.length !== 1) {
    throw new Error(
      `expected one Lambda for ${result.slug}/${example.appName}/${environment}, found ${names.length}: ${names.join(", ")}`,
    );
  }
  return names[0]!;
}

async function assertWorkerPath(
  result: Result,
  example: Example,
  baseUrl: string,
) {
  const host = new URL(baseUrl).hostname.toLowerCase();
  if (/\.lambda-url\.[a-z0-9-]+\.on\.aws$/.test(host)) {
    throw new Error(
      `${baseUrl} is an IAM-gated Lambda Function URL, not the edge worker hostname`,
    );
  }

  const deadline = Date.now() + 180_000;
  let response: Response | undefined;
  while (Date.now() < deadline) {
    try {
      const candidate = await fetch(`${baseUrl}/api/health`);
      if (candidate.status === 200) {
        response = candidate;
        break;
      }
    } catch {}
    await delay(5_000);
  }
  if (!response) throw new Error(`${baseUrl}/api/health never served a 200`);
  if (!response.headers.get("cf-ray")) {
    throw new Error(`${baseUrl} answered without a cf-ray header`);
  }

  const name = functionName(result, example);
  let raw: string;
  try {
    raw = aws([
      "lambda",
      "get-function-url-config",
      "--function-name",
      name,
      "--output",
      "json",
    ]);
  } catch (error) {
    const stderr = String((error as { stderr?: unknown }).stderr ?? "");
    if (stderr.includes("ResourceNotFoundException")) return;
    throw error;
  }
  const functionUrl = JSON.parse(raw) as {
    AuthType?: string;
    FunctionUrl?: string;
  };
  if (functionUrl.AuthType !== "AWS_IAM" || !functionUrl.FunctionUrl) {
    throw new Error(
      `${name} publishes a Function URL with AuthType ${functionUrl.AuthType ?? "missing"}`,
    );
  }
  const direct = await fetch(functionUrl.FunctionUrl).catch((error: Error) => ({
    status: `fetch failed: ${error.message}`,
  }));
  if (direct.status !== 403) {
    throw new Error(
      `unsigned request to ${functionUrl.FunctionUrl} answered ${direct.status}, not 403`,
    );
  }
}

export function projectSlugForRun() {
  const raw = process.env.GITHUB_RUN_ID ?? "local";
  const run = raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 30);
  return `e2ec-${run || "local"}`;
}

export function createAwsTarget(token: string): Target {
  const slug = process.env.OCEL_TEST_PROJECT_SLUG ?? projectSlugForRun();
  return {
    name: "aws",
    async up(example) {
      const env = childEnv(token, slug);
      const ref = `conformance-${example.name}`;
      let createdEnv = false;
      if (example.capabilities.includes("env")) {
        try {
          await writeFile(path.join(example.dir, ".env"), nextDotenv(), {
            flag: "wx",
          });
          createdEnv = true;
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
        }
      }
      try {
        await runOcel("ocel build", ["build"], example, env);
        await runOcel(
          "ocel preview up",
          ["preview", "up", "--ref", ref, "--prebuilt", "--no-ui"],
          example,
          env,
        );
        const result = JSON.parse(
          await readFile(path.join(example.dir, deployResult), "utf8"),
        ) as Result;
        if (result.appUrls.length !== 1 || !result.appUrls[0]) {
          throw new Error(
            `${example.name} deploy returned appUrls ${JSON.stringify(result.appUrls)}`,
          );
        }
        const baseUrl = result.appUrls[0];
        await assertWorkerPath(result, example, baseUrl);
        return {
          baseUrl,
          teardown: async () => {
            try {
              await runOcel(
                "ocel preview rm",
                ["preview", "rm", "--ref", ref, "--yes"],
                example,
                env,
              );
            } finally {
              if (createdEnv) {
                await rm(path.join(example.dir, ".env"), { force: true });
              }
            }
          },
        };
      } catch (error) {
        if (createdEnv) await rm(path.join(example.dir, ".env"), { force: true });
        throw error;
      }
    },
  };
}

export const awsTargetEnv = childEnv;
export const runAwsOcel = runOcel;
