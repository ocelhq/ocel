import type { TagRecords } from "../src/next/tag-records.mjs";

const instanceCount = 20;
const tagsPerInstance = 25;
const readEveryMs = 2000;
const publishSpreadMs = 1000;

export interface TagBurstOptions {
  newTags: (instance: number) => TagRecords;
  wait: (ms: number) => Promise<void>;
  durationMs: number;
}

export interface TagBurst {
  publishes: number;
  resolveMs: number[];
  visibleElsewhereMs: number[];
  unseen: string[];
  tags: string[];
}

export function percentile(values: number[], p: number): number {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(p * sorted.length) - 1)]!;
}

export function reportTimings(label: string, values: number[]): void {
  console.log(
    `${label}: p50=${percentile(values, 0.5)}ms p95=${percentile(values, 0.95)}ms max=${Math.max(...values)}ms`,
  );
}

export async function driveTagBurst(options: TagBurstOptions): Promise<TagBurst> {
  const started = Date.now();
  const instances = Array.from({ length: instanceCount }, (_, id) => ({
    id,
    tags: options.newTags(id),
    cursor: null as string | null,
    seenAt: new Map<string, number>(),
  }));

  const publishedAt = new Map<string, number>();
  const resolveMs: number[] = [];
  const publishes: Promise<unknown>[] = [];
  const publish = (instance: (typeof instances)[number], tag: string) => {
    const at = Date.now() - started;
    publishedAt.set(`${instance.id}:${tag}`, at);
    publishes.push(
      instance.tags
        .publish(tag, { expired: at + 1 })
        .then(() => resolveMs.push(Date.now() - started - at)),
    );
  };

  for (const instance of instances) {
    publish(instance, "products");
    for (let j = 0; j < tagsPerInstance; j++) {
      setTimeout(
        () => publish(instance, `i${instance.id}-t${j}`),
        Math.floor((j * publishSpreadMs) / tagsPerInstance),
      );
    }
    const phase = (instance.id * readEveryMs) / instanceCount;
    setTimeout(function tick() {
      void instance.tags.read(instance.cursor).then((read) => {
        if (read.status !== "fresh") return;
        instance.cursor = read.cursor;
        for (const tag of Object.keys(read.records)) {
          if (!instance.seenAt.has(tag)) instance.seenAt.set(tag, Date.now() - started);
        }
      });
      if (Date.now() - started < options.durationMs) setTimeout(tick, readEveryMs);
    }, phase);
  }

  await options.wait(options.durationMs);
  await Promise.all(publishes);

  const visibleElsewhereMs: number[] = [];
  const unseen: string[] = [];
  const tags = ["products"];
  for (const instance of instances) {
    for (let j = 0; j < tagsPerInstance; j++) {
      const tag = `i${instance.id}-t${j}`;
      tags.push(tag);
      for (const other of instances) {
        if (other === instance) continue;
        const seen = other.seenAt.get(tag);
        if (seen === undefined) {
          unseen.push(`${other.id} never read ${tag}`);
          continue;
        }
        visibleElsewhereMs.push(seen - publishedAt.get(`${instance.id}:${tag}`)!);
      }
    }
  }

  return { publishes: publishes.length, resolveMs, visibleElsewhereMs, unseen, tags };
}
