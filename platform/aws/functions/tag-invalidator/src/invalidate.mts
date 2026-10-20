import { createHash } from "node:crypto";

import {
  derivePurgeSecret,
  PurgeRefusedError,
  type PurgeSender,
  postPurge,
} from "@platform/edge-contract/purge";
import { coordinateOf, type Raise, type Raises } from "./records.mjs";
import { invalidationBatches } from "./tags.mjs";
import {
  type DynamoCommands,
  type DynamoLike,
  readCloudflareTargets,
  targetsOf,
} from "./targets.mjs";

export interface CloudFrontLike {
  send(command: any): Promise<any>;
}

export interface Commands extends DynamoCommands {
  CreateInvalidationCommand: new (input: any) => any;
}

export interface Invalidator {
  cloudfront: CloudFrontLike;
  dynamo: DynamoLike;
  commands: Commands;
  table: string;
  bootstrapTier: string;
  sleep?: (ms: number) => Promise<void>;
  cloudflare?: PurgeSender & { readSeed(): Promise<string> };
}

interface ProjectTargets {
  cloudfront: string[];
  cloudflare: string[];
}

const alreadySent = "InvalidationBatchAlreadyExists";

const goneFront = "NoSuchDistribution";

const goneWorkerStatus = 404;

const congested = [
  "TooManyInvalidationsInProgress",
  "Throttling",
  "ThrottlingException",
  "TooManyRequests",
  "RequestLimitExceeded",
  "ServiceUnavailable",
  "SlowDown",
];

const congestionAttempts = 6;

const congestionBackoffMs = 500;

const congestionCeilingMs = 20_000;

const buildsAtOnce = 4;

function codeOf(error: unknown): string {
  const named = error as { name?: string } | null;
  return typeof named?.name === "string" ? named.name : "";
}

function backoff(attempt: number): number {
  const ceiling = Math.min(congestionBackoffMs * 2 ** attempt, congestionCeilingMs);
  return Math.round(Math.random() * ceiling);
}

async function settledPool(
  count: number,
  width: number,
  run: (i: number) => Promise<void>,
): Promise<PromiseSettledResult<void>[]> {
  const results = new Array<PromiseSettledResult<void>>(count);
  let next = 0;
  const worker = async () => {
    for (let i = next++; i < count; i = next++) {
      try {
        await run(i);
        results[i] = { status: "fulfilled", value: undefined };
      } catch (reason) {
        results[i] = { status: "rejected", reason };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(width, count) }, worker));
  return results;
}

function refuseIfAny(refused: readonly unknown[], message: string): void {
  if (refused.length > 0) throw new AggregateError(refused, message);
}

async function settleEvery(work: readonly Promise<void>[], message: string): Promise<void> {
  const results = await Promise.allSettled(work);
  refuseIfAny(
    results.flatMap((result) => (result.status === "rejected" ? [result.reason] : [])),
    message,
  );
}

function callerReference(release: string, raise: Raise, paths: readonly string[]): string {
  const content = [release, ...[...raise.sequenceNumbers].sort(), ...paths].join("\n");
  return `ocel-${createHash("sha256").update(content).digest("hex").slice(0, 40)}`;
}

async function reach(
  inv: Invalidator,
  distribution: string,
  reference: string,
  paths: readonly string[],
): Promise<void> {
  const pause = inv.sleep ?? ((ms: number) => new Promise((done) => setTimeout(done, ms)));
  for (let attempt = 0; ; attempt++) {
    try {
      await inv.cloudfront.send(
        new inv.commands.CreateInvalidationCommand({
          DistributionId: distribution,
          InvalidationBatch: {
            CallerReference: reference,
            Paths: { Quantity: paths.length, Items: [...paths] },
          },
        }),
      );
      return;
    } catch (error) {
      const code = codeOf(error);
      if (code === alreadySent) return;
      if (!congested.includes(code) || attempt >= congestionAttempts - 1) throw error;
      await pause(backoff(attempt));
    }
  }
}

async function invalidateCloudFront(
  inv: Invalidator,
  targets: readonly string[],
  release: string,
  raise: Raise,
): Promise<void> {
  if (targets.length === 0) return;
  const { batches, dropped } = invalidationBatches(release, raise.tags);
  if (dropped.length > 0) {
    console.warn(
      `ocel: ${dropped.length} tag(s) of release ${release} are longer or wider than a cache tag CloudFront stores, so nothing invalidates them: ${dropped.join(", ")}`,
    );
  }

  const live = new Set(targets);
  const refused: unknown[] = [];
  for (const paths of batches) {
    const reference = callerReference(release, raise, paths);
    const reached = [...live];
    const results = await Promise.allSettled(
      reached.map((distribution) => reach(inv, distribution, reference, paths)),
    );
    results.forEach((result, i) => {
      if (result.status === "fulfilled") return;
      const distribution = reached[i]!;
      if (codeOf(result.reason) === goneFront) {
        live.delete(distribution);
        console.warn(
          `ocel: ${distribution} no longer answers an invalidation, so this raise skips it; the CloudFront router still names it`,
          result.reason,
        );
        return;
      }
      refused.push(result.reason);
    });
  }
  refuseIfAny(refused, "some fronts refused this invalidation");
}

async function purgeCloudflareTargets(
  inv: Invalidator,
  readSeed: () => Promise<string>,
  urls: readonly string[],
  pagesPrefix: string,
  raise: Raise,
): Promise<void> {
  if (urls.length === 0 || inv.cloudflare === undefined) return;
  const secret = await derivePurgeSecret(await readSeed(), pagesPrefix);
  const results = await Promise.allSettled(
    urls.map((url) => postPurge(inv.cloudflare!, url, secret, pagesPrefix, raise.entries)),
  );
  const refused: unknown[] = [];
  results.forEach((result, i) => {
    if (result.status === "fulfilled") return;
    const { reason } = result;
    if (reason instanceof PurgeRefusedError && reason.status === goneWorkerStatus) {
      console.warn(
        `ocel: no worker answers purges at ${urls[i]} any longer, so this raise skips it; the project still names it as an invalidation target`,
        reason,
      );
      return;
    }
    refused.push(reason);
  });
  refuseIfAny(refused, "some Cloudflare targets refused this purge");
}

async function invalidateOne(
  inv: Invalidator,
  readSeed: () => Promise<string>,
  targets: ProjectTargets,
  coordinate: { release: string; pagesPrefix: string },
  raise: Raise,
): Promise<void> {
  await settleEvery(
    [
      invalidateCloudFront(inv, targets.cloudfront, coordinate.release, raise),
      purgeCloudflareTargets(inv, readSeed, targets.cloudflare, coordinate.pagesPrefix, raise),
    ],
    "some fronts refused this invalidation",
  );
}

async function readTargets(inv: Invalidator, project: string): Promise<ProjectTargets> {
  const [cloudfront, cloudflare] = await Promise.all([
    targetsOf(inv.dynamo, inv.commands, inv.table, project),
    inv.cloudflare === undefined
      ? []
      : readCloudflareTargets(inv.dynamo, inv.commands, inv.table, project),
  ]);
  if (cloudfront.length === 0 && cloudflare.length === 0) {
    console.warn(
      `ocel: the ${inv.bootstrapTier} bootstrap names no CloudFront or Cloudflare front to invalidate for ${project}, so its raised tags reach nothing`,
    );
  }
  return { cloudfront, cloudflare };
}

export async function invalidateAll(inv: Invalidator, raises: Raises): Promise<string[]> {
  const targets = new Map<string, Promise<ProjectTargets>>();
  const builds = [...raises];
  let seed: Promise<string> | undefined;
  const readSeed = () => (seed ??= inv.cloudflare!.readSeed());

  const results = await settledPool(builds.length, buildsAtOnce, async (i) => {
    const [pagesPrefix, raise] = builds[i]!;
    const coordinate = coordinateOf(pagesPrefix);
    if (coordinate === null) {
      console.warn(
        `ocel: ${pagesPrefix} names no project and release, so the tags it raises reach no front`,
      );
      return;
    }
    const { project, release } = coordinate;

    let projectTargets = targets.get(project);
    if (projectTargets === undefined) {
      projectTargets = readTargets(inv, project);
      targets.set(project, projectTargets);
    }
    await invalidateOne(inv, readSeed, await projectTargets, { release, pagesPrefix }, raise);
  });

  return builds.flatMap(([pagesPrefix, raise], i) => {
    const result = results[i]!;
    if (result.status === "fulfilled") return [];
    console.error(`ocel: invalidating ${pagesPrefix} failed`, result.reason);
    return raise.sequenceNumbers;
  });
}
