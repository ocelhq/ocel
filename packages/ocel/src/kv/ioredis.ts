import type { Redis, RedisOptions } from "ioredis";

const ioredis = await import("ioredis").then(
  (module) => ({ module }),
  (error: unknown) => ({ error }),
);

function isIoredisMissing(error: unknown): boolean {
  for (let cause = error; cause instanceof Error; cause = cause.cause) {
    const code = (cause as { code?: unknown }).code;
    if (
      (code === "ERR_MODULE_NOT_FOUND" || code === "MODULE_NOT_FOUND") &&
      /['"]ioredis['"]/.test(cause.message)
    ) {
      return true;
    }
  }
  return false;
}

export function newRedis(options: RedisOptions): Redis {
  if ("error" in ioredis) {
    if (isIoredisMissing(ioredis.error)) {
      throw new Error(
        "a kv store is reached with ioredis, which is not installed. Install it: npm install ioredis",
        { cause: ioredis.error },
      );
    }
    throw ioredis.error;
  }
  return new ioredis.module.Redis(options);
}
