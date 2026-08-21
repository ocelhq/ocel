import { createHash } from "node:crypto";

import { coordinateOf, type Raise, type Raises } from "./records.mjs";
import { invalidationBatches } from "./tags.mjs";
import { targetsOf, type DynamoCommands, type DynamoLike } from "./targets.mjs";

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
  bootstrapClass: string;
  sleep?: (ms: number) => Promise<void>;
}

const alreadySent = "InvalidationBatchAlreadyExists";

const goneFront = "NoSuchDistribution";

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

function callerReference(release: string, raise: Raise, paths: readonly string[]): string {
  const content = [release, ...[...raise.sequenceNumbers].sort(), ...paths].join("\n");
  return "ocel-" + createHash("sha256").update(content).digest("hex").slice(0, 40);
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

async function invalidateOne(
  inv: Invalidator,
  targets: readonly string[],
  release: string,
  raise: Raise,
): Promise<void> {
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
          `ocel: ${distribution} no longer answers an invalidation, so this raise skips it; the ledger still names it`,
          result.reason,
        );
        return;
      }
      refused.push(result.reason);
    });
  }
  if (refused.length > 0) {
    throw new AggregateError(refused, "some fronts refused this invalidation");
  }
}

export async function invalidateAll(inv: Invalidator, raises: Raises): Promise<string[]> {
  const targets = new Map<string, Promise<string[]>>();
  const builds = [...raises];

  const results = await settledPool(builds.length, buildsAtOnce, async (i) => {
    const [isrPrefix, raise] = builds[i]!;
    const coordinate = coordinateOf(isrPrefix);
    if (coordinate === null) {
      console.warn(
        `ocel: ${isrPrefix} names no project and release, so the tags it raises reach no front`,
      );
      return;
    }
    const { project, release } = coordinate;

    let held = targets.get(project);
    if (held === undefined) {
      held = targetsOf(inv.dynamo, inv.commands, inv.table, inv.bootstrapClass, project);
      targets.set(project, held);
    }
    await invalidateOne(inv, await held, release, raise);
  });

  return builds.flatMap(([isrPrefix, raise], i) => {
    const result = results[i]!;
    if (result.status === "fulfilled") return [];
    console.error(`ocel: invalidating ${isrPrefix} failed`, result.reason);
    return raise.sequenceNumbers;
  });
}
