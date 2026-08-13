export const REGION = process.env.BENCH_REGION || process.env.AWS_REGION || "us-east-1";

export const PINNED = Object.freeze({
  runtime: "nodejs24.x",
  memoryMB: 1024,
  architecture: "x86_64",
});

export const APPS = Object.freeze([
  Object.freeze({ name: "express", framework: "express" }),
  Object.freeze({ name: "hono", framework: "hono" }),
  Object.freeze({ name: "fastify", framework: "fastify" }),
]);

export const PLATFORMS = Object.freeze([
  Object.freeze({ id: "ocel-bundle", driver: "ocel", env: Object.freeze({}) }),
  Object.freeze({
    id: "ocel-bundle-bytecode",
    driver: "ocel",
    env: Object.freeze({ OCEL_BYTECODE_CACHE: "1" }),
  }),
  Object.freeze({
    id: "ocel-trace",
    driver: "ocel",
    env: Object.freeze({ OCEL_BUILD_PREFER_TRACING: "1" }),
  }),
  Object.freeze({
    id: "ocel-trace-bytecode",
    driver: "ocel",
    env: Object.freeze({ OCEL_BUILD_PREFER_TRACING: "1", OCEL_BYTECODE_CACHE: "1" }),
  }),
  Object.freeze({ id: "sst", driver: "sst", env: Object.freeze({}) }),
  Object.freeze({ id: "raw", driver: "raw", env: Object.freeze({}) }),
]);

export const SAMPLES = Object.freeze({
  cold: Number(process.env.BENCH_COLD_SAMPLES ?? 10),
  warm: Number(process.env.BENCH_WARM_SAMPLES ?? 200),
  warmup: Number(process.env.BENCH_WARMUP_REQUESTS ?? 5),
  logSettleMs: Number(process.env.BENCH_LOG_SETTLE_MS ?? 25_000),
});

export const PROBE_ROUTE = "/health";
