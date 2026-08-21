import { execFileSync } from "node:child_process";
import { readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { HeadObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { createBytecodeAssertions } from "../aws-bytecode";
import { nextDotenv } from "../env";
import { repoRoot } from "../examples";
import { provisionExternalLinks, type ExternalLinks } from "../aws-links";
import { refusalProblem } from "../links";
import { run, successful } from "../process";
import type { Example, Target } from "../types";

const deployResult = path.join(".ocel", "deploy-result.json");
const config = "ocel.aws.config.ts";
const refusalConfig = ".ocel.conformance-refused.config.ts";
const skipDriftChecks = {
  OCEL_SKIP_EDGE_RECONCILE: "1",
  OCEL_SKIP_TEARDOWN_REFRESH: "1",
};

type Result = {
  slug: string;
  environment: { class: string; identity?: string };
  appUrls: string[];
  apps?: Array<{ name?: string; buildId?: string }>;
};

function ocelCommand() {
  if (process.env.OCEL_BIN) return [process.env.OCEL_BIN, []] as const;
  return [
    process.execPath,
    [path.join(repoRoot, "packages", "ocel", "bin", "run.js")],
  ] as const;
}

function childEnv(
  token: string,
  slug: string,
  bootstrapToken: string,
  example: Example,
): NodeJS.ProcessEnv {
  return {
    ...process.env,
    FIXTURE_BOOTSTRAP_TOKEN: bootstrapToken,
    OCEL_ACCESS_TOKEN: token,
    OCEL_CONFIG: config,
    OCEL_EDGE_OBSERVABILITY: "off",
    OCEL_TEST_PROJECT_SLUG: slug,
    ...(example.capabilities.includes("bytecode")
      ? { OCEL_BYTECODE_CACHE: "1", OCEL_BYTECODE_EMBED: "1" }
      : {}),
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

function resourceArns(
  result: Result,
  example: Example,
  resourceType: string,
) {
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
      resourceType,
      "--output",
      "json",
    ]),
  ) as { ResourceTagMappingList?: Array<{ ResourceARN?: string }> };
  return {
    arns: (response.ResourceTagMappingList ?? []).flatMap((entry) =>
      entry.ResourceARN ? [entry.ResourceARN] : [],
    ),
    environment,
  };
}

function functionName(result: Result, example: Example) {
  const { arns, environment } = resourceArns(
    result,
    example,
    "lambda:function",
  );
  const names = arns.flatMap((arn) => {
    const match = /^arn:aws:lambda:[^:]*:[^:]*:function:([^:]+)/.exec(
      arn,
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

function bucketName(result: Result, example: Example) {
  const { arns, environment } = resourceArns(result, example, "s3:bucket");
  const names = arns.flatMap((arn) => {
    const match = /^arn:aws:s3:::([^/]+)$/.exec(arn);
    return match?.[1] ? [match[1]] : [];
  });
  if (names.length !== 1) {
    throw new Error(
      `expected one bucket for ${result.slug}/${example.appName}/${environment}, found ${names.length}: ${names.join(", ")}`,
    );
  }
  return names[0]!;
}

async function assertWorkerPath(
  baseUrl: string,
  name: string,
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

async function bootstrapFixture(baseUrl: string, token: string) {
  const response = await fetch(`${baseUrl}/api/bootstrap`, {
    method: "POST",
    headers: { authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    throw new Error(
      `${baseUrl}/api/bootstrap answered ${response.status}: ${await response.text()}`,
    );
  }
}

function refusalConfigSource() {
  return `import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import base from "./ocel.config";

export default defineConfig({
  ...base,
  links: [],
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  edge: cloudflare(),
});
`;
}

async function assertRefused(
  example: Example,
  ref: string,
  env: NodeJS.ProcessEnv,
) {
  const configPath = path.join(example.dir, refusalConfig);
  await writeFile(configPath, refusalConfigSource(), { flag: "wx" });
  const refusedEnv = { ...env, OCEL_CONFIG: refusalConfig };
  try {
    await runOcel("ocel refusal build", ["build"], example, refusedEnv);
    const [command, prefix] = ocelCommand();
    const result = await run(
      command,
      [
        ...prefix,
        "preview",
        "up",
        "--ref",
        ref,
        "--prebuilt",
        "--no-ui",
      ],
      { cwd: example.dir, env: refusedEnv, timeoutMs: 25 * 60_000 },
    );
    const problem = refusalProblem(
      result.code,
      `${result.stdout}\n${result.stderr}`,
    );
    if (problem) {
      if (result.code === 0) {
        await runOcel(
          "ocel refusal cleanup",
          ["preview", "rm", "--ref", ref, "--yes"],
          example,
          refusedEnv,
        );
      }
      throw new Error(problem);
    }
  } finally {
    await rm(configPath, { force: true });
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
      const bootstrapToken = crypto.randomUUID();
      const env = childEnv(token, slug, bootstrapToken, example);
      const ref = `conformance-${example.name}`;
      let createdEnv = false;
      let external: ExternalLinks | undefined;
      let deployed = false;
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
        if (example.linkTool) {
          external = provisionExternalLinks(example, slug, ref);
          await external.provision();
          await external.assertPublished();
        }
        const build = await runOcel("ocel build", ["build"], example, env);
        const deploy = await runOcel(
          "ocel preview up",
          ["preview", "up", "--ref", ref, "--prebuilt", "--no-ui"],
          example,
          env,
        );
        deployed = true;
        const result = JSON.parse(
          await readFile(path.join(example.dir, deployResult), "utf8"),
        ) as Result;
        if (result.appUrls.length !== 1 || !result.appUrls[0]) {
          throw new Error(
            `${example.name} deploy returned appUrls ${JSON.stringify(result.appUrls)}`,
          );
        }
        const baseUrl = result.appUrls[0];
        const name = functionName(result, example);
        await assertWorkerPath(baseUrl, name);
        const bytecode = example.capabilities.includes("bytecode")
          ? createBytecodeAssertions(result, example.appName, name, baseUrl)
          : undefined;
        if (!example.capabilities.includes("links")) {
          await bootstrapFixture(baseUrl, bootstrapToken);
        }
        await external?.assertConsumed(example.appName);
        const objectStore = new S3Client({
          maxAttempts: 4,
          retryMode: "standard",
        });
        return {
          baseUrl,
          output: () =>
            [build.stdout, build.stderr, deploy.stdout, deploy.stderr].join(
              "\n",
            ),
          linkReport: external?.report,
          headObject: async (key) => {
            const metadata = await objectStore.send(
              new HeadObjectCommand({
                Bucket: bucketName(result, example),
                Key: key,
              }),
            );
            return { contentType: metadata.ContentType };
          },
          assertBytecodeArchive:
            bytecode?.archive ?? unsupportedBytecodeAssertion,
          assertBytecodeEmbeddedArtifact:
            bytecode?.artifact ?? unsupportedBytecodeAssertion,
          assertBytecodeColdStart:
            bytecode?.coldStart ?? unsupportedBytecodeAssertion,
          teardown: async () => {
            objectStore.destroy();
            try {
              await runOcel(
                "ocel preview rm",
                ["preview", "rm", "--ref", ref, "--yes"],
                example,
                env,
              );
              external?.assertConsumerRemoved();
            } finally {
              try {
                if (external) {
                  await external.teardown();
                  external.assertPublisherRemoved();
                  await assertRefused(example, ref, env);
                }
              } finally {
                if (createdEnv) {
                  await rm(path.join(example.dir, ".env"), { force: true });
                }
              }
            }
          },
        };
      } catch (error) {
        try {
          if (deployed) {
            await runOcel(
              "ocel preview rm",
              ["preview", "rm", "--ref", ref, "--yes"],
              example,
              env,
            );
          }
        } finally {
          try {
            await external?.teardown();
          } finally {
            if (createdEnv) {
              await rm(path.join(example.dir, ".env"), { force: true });
            }
          }
        }
        throw error;
      }
    },
  };
}

async function unsupportedBytecodeAssertion() {
  throw new Error("example does not declare bytecode conformance");
}
