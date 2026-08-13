import { execFileSync } from "node:child_process";

export const AWS_TIMEOUT_MS = 30_000;

export const AWS_CLI_RETRY_ENV = Object.freeze({ AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" });

export const COLD_NONCE_ENV = "OCEL_BENCH_COLD_NONCE";

export const UPDATE_DEADLINE_MS = 120_000;

export const UPDATE_POLL_MS = 1_000;

export function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function aws(args, { region } = {}) {
  const full = region ? [...args, "--region", region] : args;
  try {
    return execFileSync("aws", full, {
      encoding: "utf8",
      timeout: AWS_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "pipe"],
      maxBuffer: 32 * 1024 * 1024,
      env: { ...process.env, ...AWS_CLI_RETRY_ENV },
    }).trim();
  } catch (err) {
    if (err.code === "ENOENT") {
      throw new Error("the aws CLI is not on PATH; install awscli v2 and re-run, or run with --dry-run");
    }
    const detail = String(err.stderr || err.message).trim().split("\n").pop();
    throw new Error(`aws ${full.slice(0, 2).join(" ")} failed: ${detail}`);
  }
}

export function awsUnreachable({ region } = {}) {
  try {
    aws(["sts", "get-caller-identity", "--query", "Account", "--output", "text"], { region });
    return null;
  } catch (err) {
    return err.message;
  }
}

export function logGroupFor(functionName) {
  return `/aws/lambda/${functionName}`;
}

export function functionConfiguration({ functionName, region }) {
  return JSON.parse(
    aws(["lambda", "get-function-configuration", "--function-name", functionName, "--output", "json"], { region }),
  );
}

export async function forceColdStart({ functionName, region, nonce = Date.now() }) {
  const current = functionConfiguration({ functionName, region });
  const variables = { ...(current.Environment?.Variables ?? {}), [COLD_NONCE_ENV]: String(nonce) };
  await applyConfiguration({ functionName, region, variables });
  return await waitForUpdate({ functionName, region });
}

async function applyConfiguration({ functionName, region, variables }) {
  const deadline = Date.now() + UPDATE_DEADLINE_MS;
  for (;;) {
    try {
      return JSON.parse(
        aws(
          [
            "lambda",
            "update-function-configuration",
            "--function-name",
            functionName,
            "--environment",
            JSON.stringify({ Variables: variables }),
            "--output",
            "json",
          ],
          { region },
        ),
      );
    } catch (err) {
      if (!err.message.includes("ResourceConflictException") || Date.now() >= deadline) {
        throw new Error(
          `could not set ${COLD_NONCE_ENV} on ${functionName}, so no cold start can be forced: ${err.message}`,
        );
      }
      await sleep(UPDATE_POLL_MS);
    }
  }
}

async function waitForUpdate({ functionName, region }) {
  const deadline = Date.now() + UPDATE_DEADLINE_MS;
  for (;;) {
    const config = functionConfiguration({ functionName, region });
    if (config.LastUpdateStatus === "Failed") {
      throw new Error(
        `${functionName} rejected the ${COLD_NONCE_ENV} update: ${config.LastUpdateStatusReason ?? "no reason given"}`,
      );
    }
    if (config.LastUpdateStatus === "Successful" && config.State === "Active") {
      return config;
    }
    if (Date.now() >= deadline) {
      throw new Error(
        `${functionName} was still ${config.LastUpdateStatus}/${config.State} after ` +
          `${Math.round(UPDATE_DEADLINE_MS / 1000)}s; a sample taken now would not be a cold start`,
      );
    }
    await sleep(UPDATE_POLL_MS);
  }
}
