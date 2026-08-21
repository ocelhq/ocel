import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { gunzipSync } from "node:zlib";
import { setTimeout as delay } from "node:timers/promises";

const archiveName = /^node(\d+\.\d+\.\d+)-([a-z0-9_]+)\.tar\.gz$/;
const embeddedMarker = "loaded embedded compile cache from ";
const s3Marker = "rehydrated compile cache from ";
const maxPackageBytes = 250 * 1024 * 1024;
const logPageLimit = 1000;
const logPollIntervalMs = 5_000;
const retryCeilingMs = 30_000;
const requestTimeoutMs = 10_000;
const maximumBackoffMs = 8_000;

type Environment = { class: string; identity?: string };

type Deployment = {
  slug: string;
  environment: Environment;
  apps?: Array<{ name?: string; buildId?: string }>;
};

type FunctionDescription = {
  Code?: { Location?: string };
  Configuration?: { Architectures?: string[]; CodeSha256?: string };
};

type LogEvent = { eventId?: string; message?: string };

type RetryDependencies = {
  now: () => number;
  random: () => number;
  sleep: (milliseconds: number) => Promise<void>;
};

export type BytecodeDependencies = RetryDependencies & {
  command: (args: string[]) => string;
  commandBytes: (args: string[]) => Buffer;
  fetch: typeof fetch;
  timeoutSignal: (milliseconds: number) => AbortSignal;
};

type BytecodeDeployment = {
  assetBucket: string;
  cacheBytes: Buffer;
  cacheKey: string;
  embeddedPath: string;
  functionName: string;
  packageBytes: Buffer;
  packageSha: string;
};

export type BytecodeAssertions = {
  archive: () => Promise<void>;
  artifact: () => Promise<void>;
  coldStart: () => Promise<void>;
};

const runtimeDependencies: BytecodeDependencies = {
  command: aws,
  commandBytes: awsBytes,
  fetch,
  now: Date.now,
  random: Math.random,
  sleep: delay,
  timeoutSignal: AbortSignal.timeout,
};

export function createBytecodeAssertions(
  result: Deployment,
  appName: string,
  functionName: string,
  baseUrl: string,
  dependencies: BytecodeDependencies = runtimeDependencies,
): BytecodeAssertions {
  let deployment: Promise<BytecodeDeployment> | undefined;
  const resolve = () =>
    (deployment ??= resolveBytecodeDeployment(
      result,
      appName,
      functionName,
      dependencies,
    ));
  return {
    archive: async () => assertCacheArchive(await resolve()),
    artifact: async () => assertEmbeddedArtifact(await resolve()),
    coldStart: async () =>
      assertEmbeddedColdStart(await resolve(), baseUrl, dependencies),
  };
}

async function resolveBytecodeDeployment(
  result: Deployment,
  appName: string,
  functionName: string,
  dependencies: BytecodeDependencies,
): Promise<BytecodeDeployment> {
  const app = result.apps?.find((candidate) => candidate.name === appName);
  if (!app?.buildId) {
    throw new Error(
      `deploy result has no build id for ${appName}: ${JSON.stringify(result.apps ?? [])}`,
    );
  }
  const assetBucket = resolveBootstrapBucket(
    "AssetBucket",
    dependencies.command,
  );
  const environment =
    result.environment.class === "preview"
      ? `preview-${result.environment.identity ?? ""}`
      : "prod";
  const keyPrefix = `${environment}/${result.slug}/${appName}/${app.buildId}/bytecode/${functionName}/`;
  const description = JSON.parse(
    dependencies.command([
      "lambda",
      "get-function",
      "--function-name",
      functionName,
      "--output",
      "json",
    ]),
  ) as FunctionDescription;
  const architecture = description.Configuration?.Architectures?.[0];
  if (!architecture) {
    throw new Error(`${functionName} has no reported architecture`);
  }
  const cacheKey = await findCacheKey(
    assetBucket,
    keyPrefix,
    architecture,
    dependencies,
  );
  const cacheBytes = await readCacheObject(
    assetBucket,
    cacheKey,
    dependencies,
  );
  const name = cacheKey.slice(keyPrefix.length);
  const embeddedPath = `.ocel/bytecode/${name.slice(0, -".gz".length)}`;
  const location = description.Code?.Location;
  const packageSha = description.Configuration?.CodeSha256;
  if (!location || !packageSha) {
    throw new Error(`${functionName} has no downloadable code location or sha`);
  }
  const packageBytes = await downloadPackage(
    location,
    functionName,
    dependencies,
  );
  if (packageBytes.length > maxPackageBytes) {
    throw new Error(
      `${functionName}'s deployment package is ${packageBytes.length} bytes`,
    );
  }
  return {
    assetBucket,
    cacheBytes,
    cacheKey,
    embeddedPath,
    functionName,
    packageBytes,
    packageSha,
  };
}

export function assertCacheArchive(deployment: BytecodeDeployment) {
  let archive: Buffer;
  try {
    archive = gunzipSync(deployment.cacheBytes);
  } catch (error) {
    throw new Error(
      `s3://${deployment.assetBucket}/${deployment.cacheKey} is not valid gzip: ${(error as Error).message}`,
    );
  }
  const entries = tarEntryNames(archive);
  if (entries.length === 0) {
    throw new Error(
      `s3://${deployment.assetBucket}/${deployment.cacheKey} contains an empty tar`,
    );
  }
  if (!entries.some((entry) => entry.includes("/"))) {
    throw new Error(
      `s3://${deployment.assetBucket}/${deployment.cacheKey} contains no nested compile-cache entry`,
    );
  }
}

export function assertEmbeddedArtifact(deployment: BytecodeDeployment) {
  const actualSha = createHash("sha256")
    .update(deployment.packageBytes)
    .digest("base64");
  if (actualSha !== deployment.packageSha) {
    throw new Error(
      `${deployment.functionName}'s downloaded package hashes to ${actualSha}, not ${deployment.packageSha}`,
    );
  }
  const entries = zipEntryNames(deployment.packageBytes);
  if (!entries.includes(deployment.embeddedPath)) {
    throw new Error(
      `${deployment.functionName}'s deployed package does not contain ${deployment.embeddedPath}`,
    );
  }
}

export async function assertEmbeddedColdStart(
  deployment: BytecodeDeployment,
  baseUrl: string,
  dependencies: BytecodeDependencies = runtimeDependencies,
) {
  const startedAt = dependencies.now();
  const requests = await Promise.all(
    Array.from({ length: 20 }, (_, index) => {
      const url = new URL("/api/revalidate", baseUrl);
      url.searchParams.set(
        "tag",
        `ocel-conformance-bytecode-${startedAt}-${process.pid}-${index}`,
      );
      return dependencies.fetch(url, { method: "POST" })
        .then((response) => response.ok)
        .catch(() => false);
    }),
  );
  if (!requests.some(Boolean)) {
    throw new Error("all cold-start probe requests failed");
  }
  const deadline = dependencies.now() + 60_000;
  const seen = new Map<string, string>();
  let finalCount = 0;
  let lastError: Error | undefined;
  do {
    try {
      const events = filterLogs(
        deployment.functionName,
        startedAt,
        dependencies.command,
      );
      finalCount = events.length;
      ingest(events, seen);
    } catch (error) {
      lastError = error as Error;
    }
    if (dependencies.now() < deadline) {
      await dependencies.sleep(logPollIntervalMs);
    }
  } while (dependencies.now() < deadline);
  const confirmationDeadline = dependencies.now() + 30_000;
  let confirmed = false;
  do {
    try {
      const events = filterLogs(
        deployment.functionName,
        startedAt,
        dependencies.command,
      );
      finalCount = events.length;
      ingest(events, seen);
      confirmed = true;
    } catch (error) {
      lastError = error as Error;
      if (dependencies.now() < confirmationDeadline) {
        await dependencies.sleep(logPollIntervalMs);
      }
    }
  } while (!confirmed && dependencies.now() < confirmationDeadline);
  if (!confirmed) {
    throw new Error(
      `could not read ${deployment.functionName}'s cold-start log window to its end: ${lastError?.message}`,
    );
  }
  if (finalCount >= logPageLimit) {
    throw new Error(
      `${deployment.functionName}'s cold-start log window reached the ${logPageLimit}-event page limit`,
    );
  }
  const messages = [...seen.values()];
  const s3Hits = messages.filter((message) => message.includes(s3Marker));
  if (s3Hits.length > 0) {
    throw new Error(
      `${s3Hits.length} cold instance(s) fell back to the S3 compile cache: ${s3Hits.slice(0, 3).join(" | ")}`,
    );
  }
  const taskPath = `/var/task/${deployment.embeddedPath}`;
  const embeddedHits = messages.filter((message) =>
    message.includes(`${embeddedMarker}${taskPath}:`),
  );
  if (embeddedHits.length === 0) {
    throw new Error(
      `no cold instance loaded the embedded compile cache from ${taskPath}`,
    );
  }
}

export async function findCacheKey(
  bucket: string,
  prefix: string,
  architecture: string,
  dependencies: Pick<
    BytecodeDependencies,
    "command" | "now" | "random" | "sleep"
  >,
) {
  const deadline = dependencies.now() + retryCeilingMs;
  let attempt = 0;
  for (;;) {
    try {
      const listed = JSON.parse(
        dependencies.command([
          "s3api",
          "list-objects-v2",
          "--bucket",
          bucket,
          "--prefix",
          prefix,
          "--output",
          "json",
        ]),
      ) as { Contents?: Array<{ Key?: string }> };
      const keys = (listed.Contents ?? []).flatMap((entry) =>
        entry.Key ? [entry.Key] : [],
      );
      const candidates = keys.filter((key) => {
        const match = archiveName.exec(key.slice(prefix.length));
        return match?.[2] === architecture;
      });
      if (candidates.length === 1) return candidates[0]!;
      if (candidates.length > 1) {
        throw new Error(
          `expected one ${architecture} compile cache under s3://${bucket}/${prefix}, found ${candidates.length}: ${candidates.join(", ")}`,
        );
      }
      throw new CacheNotReadyError(
        `no ${architecture} compile cache under s3://${bucket}/${prefix}`,
      );
    } catch (error) {
      if (!isTransientAwsRead(error)) throw error;
      await backoff(error, attempt++, deadline, dependencies);
    }
  }
}

export async function downloadPackage(
  location: string,
  functionName: string,
  dependencies: Pick<
    BytecodeDependencies,
    "fetch" | "now" | "random" | "sleep" | "timeoutSignal"
  >,
) {
  const deadline = dependencies.now() + retryCeilingMs;
  let attempt = 0;
  for (;;) {
    const remaining = deadline - dependencies.now();
    if (remaining <= 0) {
      throw new Error(
        `could not download ${functionName}'s deployment package within ${retryCeilingMs / 1000}s`,
      );
    }
    let response: Response;
    try {
      response = await dependencies.fetch(location, {
        signal: dependencies.timeoutSignal(
          Math.min(requestTimeoutMs, remaining),
        ),
      });
      if (response.ok) {
        return Buffer.from(await response.arrayBuffer());
      }
    } catch (error) {
      if (!isTransientFetchError(error)) {
        throw new Error(
          `could not download ${functionName}'s deployment package: ${(error as Error).message}`,
        );
      }
      await backoff(error, attempt++, deadline, dependencies);
      continue;
    }
    const retryAfter = retryAfterMilliseconds(
      response.headers.get("retry-after"),
      dependencies.now(),
    );
    if (!isTransientHttpStatus(response.status)) {
      throw new Error(
        `could not download ${functionName}'s deployment package: HTTP ${response.status}`,
      );
    }
    await response.body?.cancel().catch(() => {});
    await backoff(
      new Error(`HTTP ${response.status}`),
      attempt++,
      deadline,
      dependencies,
      retryAfter,
    );
  }
}

export async function readCacheObject(
  bucket: string,
  key: string,
  dependencies: Pick<
    BytecodeDependencies,
    "commandBytes" | "now" | "random" | "sleep"
  >,
) {
  const deadline = dependencies.now() + retryCeilingMs;
  let attempt = 0;
  for (;;) {
    try {
      return dependencies.commandBytes([
        "s3",
        "cp",
        `s3://${bucket}/${key}`,
        "-",
      ]);
    } catch (error) {
      if (!isTransientAwsRead(error)) throw error;
      await backoff(error, attempt++, deadline, dependencies);
    }
  }
}

class CacheNotReadyError extends Error {}

function isTransientAwsRead(error: unknown) {
  if (error instanceof CacheNotReadyError) return true;
  const detail = `${String((error as { message?: unknown }).message ?? "")} ${String(
    (error as { stderr?: unknown }).stderr ?? "",
  )}`;
  return /(?:SlowDown|RequestTimeout|Throttl|TooManyRequests|ServiceUnavailable|InternalError|ECONNRESET|ETIMEDOUT|EAI_AGAIN|NoSuchKey|\bNotFound\b)/i.test(
    detail,
  );
}

function isTransientFetchError(error: unknown) {
  const candidate = error as {
    name?: unknown;
    message?: unknown;
    cause?: unknown;
  };
  const detail = `${String(candidate.name ?? "")} ${String(candidate.message ?? "")} ${String(candidate.cause ?? "")}`;
  return /(?:AbortError|TimeoutError|TypeError|ECONNRESET|ETIMEDOUT|EAI_AGAIN|ENETUNREACH|fetch failed)/i.test(
    detail,
  );
}

function isTransientHttpStatus(status: number) {
  return (
    status === 408 ||
    status === 425 ||
    status === 429 ||
    (status >= 500 && status <= 599)
  );
}

function retryAfterMilliseconds(value: string | null, now: number) {
  if (!value) return undefined;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000;
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - now) : undefined;
}

async function backoff(
  error: unknown,
  attempt: number,
  deadline: number,
  dependencies: RetryDependencies,
  retryAfter?: number,
) {
  const exponential = Math.min(maximumBackoffMs, 250 * 2 ** attempt);
  const jittered = exponential * (0.5 + dependencies.random() * 0.5);
  const wait = Math.ceil(Math.max(jittered, retryAfter ?? 0));
  if (dependencies.now() + wait >= deadline) throw error;
  await dependencies.sleep(wait);
}

function ingest(events: LogEvent[], seen: Map<string, string>) {
  for (const event of events) {
    const message = String(event.message ?? "");
    seen.set(event.eventId ?? message, message);
  }
}

function resolveBootstrapBucket(
  logicalId: string,
  command: (args: string[]) => string,
) {
  const bucket = command([
    "cloudformation",
    "describe-stack-resources",
    "--stack-name",
    process.env.OCEL_BOOTSTRAP_STACK ?? "ocel-bootstrap-preview",
    "--query",
    `StackResources[?LogicalResourceId==\`${logicalId}\`].PhysicalResourceId | [0]`,
    "--output",
    "text",
  ]);
  if (!bucket || bucket === "None") {
    throw new Error(`could not resolve the substrate's ${logicalId}`);
  }
  return bucket;
}

function filterLogs(
  functionName: string,
  startTime: number,
  command: (args: string[]) => string,
) {
  const pattern = `?"embedded compile cache" ?"${s3Marker.trim()}"`;
  const response = JSON.parse(
    command([
      "logs",
      "filter-log-events",
      "--log-group-name",
      `/aws/lambda/${functionName}`,
      "--start-time",
      String(startTime),
      "--filter-pattern",
      pattern,
      "--limit",
      String(logPageLimit),
      "--output",
      "json",
    ]),
  ) as { events?: LogEvent[] };
  return response.events ?? [];
}

function aws(args: string[]) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: 30_000,
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  }).trim();
}

function awsBytes(args: string[]) {
  return execFileSync("aws", args, {
    timeout: 30_000,
    maxBuffer: 128 * 1024 * 1024,
    env: { ...process.env, AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  });
}

function tarString(block: Buffer, start: number, length: number) {
  const slice = block.subarray(start, start + length);
  const end = slice.indexOf(0);
  return Buffer.from(end === -1 ? slice : slice.subarray(0, end)).toString(
    "utf8",
  );
}

function tarEntryNames(buffer: Buffer) {
  const names: string[] = [];
  let offset = 0;
  let sawEnd = false;
  while (offset < buffer.length) {
    if (offset + 512 > buffer.length) {
      throw new Error(`truncated tar header at byte ${offset}`);
    }
    const header = buffer.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) {
      sawEnd = true;
      break;
    }
    const expected = parseInt(tarString(header, 148, 8).trim(), 8);
    let actual = 0;
    for (let index = 0; index < header.length; index++) {
      actual += index >= 148 && index < 156 ? 0x20 : header[index]!;
    }
    if (!Number.isInteger(expected) || actual !== expected) {
      throw new Error(`tar header at byte ${offset} fails its checksum`);
    }
    const name = tarString(header, 0, 100);
    const prefix = tarString(header, 345, 155);
    const size = parseInt(tarString(header, 124, 12).trim() || "0", 8) || 0;
    names.push(prefix ? `${prefix}/${name}` : name);
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  if (!sawEnd) throw new Error("tar has no end-of-archive marker");
  return names;
}

function zipEntryNames(buffer: Buffer) {
  const minimum = 22;
  const floor = Math.max(0, buffer.length - minimum - 0xffff);
  let eocd = -1;
  for (let index = buffer.length - minimum; index >= floor; index--) {
    if (buffer.readUInt32LE(index) === 0x06054b50) {
      eocd = index;
      break;
    }
  }
  if (eocd < 0) throw new Error("deployment package is not a zip");
  let count = buffer.readUInt16LE(eocd + 10);
  let offset = buffer.readUInt32LE(eocd + 16);
  if (count === 0xffff || offset === 0xffffffff) {
    const locator = eocd - 20;
    if (locator < 0 || buffer.readUInt32LE(locator) !== 0x07064b50) {
      throw new Error("saturated zip has no zip64 locator");
    }
    const zip64 = asIndex(
      buffer.readBigUInt64LE(locator + 8),
      buffer.length,
      "zip64 directory offset",
    );
    if (buffer.readUInt32LE(zip64) !== 0x06064b50) {
      throw new Error(`no zip64 directory at byte ${zip64}`);
    }
    count = asIndex(
      buffer.readBigUInt64LE(zip64 + 32),
      Number.MAX_SAFE_INTEGER,
      "zip64 entry count",
    );
    offset = asIndex(
      buffer.readBigUInt64LE(zip64 + 48),
      buffer.length,
      "zip64 central directory offset",
    );
  }
  const names: string[] = [];
  for (let index = 0; index < count; index++) {
    if (
      offset + 46 > buffer.length ||
      buffer.readUInt32LE(offset) !== 0x02014b50
    ) {
      throw new Error(`invalid zip central directory entry ${index}`);
    }
    const nameLength = buffer.readUInt16LE(offset + 28);
    const extraLength = buffer.readUInt16LE(offset + 30);
    const commentLength = buffer.readUInt16LE(offset + 32);
    const nameAt = offset + 46;
    if (nameAt + nameLength > buffer.length) {
      throw new Error(`zip entry ${index} name runs past the package`);
    }
    names.push(buffer.toString("utf8", nameAt, nameAt + nameLength));
    offset = nameAt + nameLength + extraLength + commentLength;
  }
  return names;
}

function asIndex(value: bigint, limit: number, name: string) {
  if (value > BigInt(limit)) {
    throw new Error(`${name} (${value}) is past the end of the package`);
  }
  return Number(value);
}
