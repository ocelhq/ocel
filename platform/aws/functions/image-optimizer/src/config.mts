import { createHash } from "node:crypto";
import type { CompiledImageConfig } from "./contract.mjs";
import { SubstrateError } from "./errors.mjs";
import { imageConfigKey } from "./keys.mjs";
import type { ObjectStore } from "./store.mjs";

const CONFIG_LIMIT = 1024 * 1024;

const MEMO_LIMIT = 64;
const memo = new Map<string, CompiledImageConfig>();

export function resetConfigMemo(): void {
  memo.clear();
}

export async function loadImageConfig(
  store: ObjectStore,
  assetPrefix: string,
  configHash: string,
): Promise<CompiledImageConfig> {
  if (!/^[0-9a-f]{64}$/.test(configHash)) {
    throw new SubstrateError("configHash is not a sha256 digest", configHash);
  }
  const memoKey = `${assetPrefix} ${configHash}`;
  const cached = memo.get(memoKey);
  if (cached) return cached;

  const key = imageConfigKey(assetPrefix);
  let object;
  try {
    object = await store.get(key, CONFIG_LIMIT);
  } catch (error) {
    throw new SubstrateError(`reading ${key}`, error);
  }
  if (!object) throw new SubstrateError(`no image config at ${key}`);

  const digest = createHash("sha256").update(object.bytes).digest("hex");
  if (digest !== configHash) {
    throw new SubstrateError(`image config at ${key} does not match configHash`, {
      expected: configHash,
      actual: digest,
    });
  }

  let config: CompiledImageConfig;
  try {
    config = JSON.parse(new TextDecoder().decode(object.bytes)) as CompiledImageConfig;
  } catch (error) {
    throw new SubstrateError(`image config at ${key} is not JSON`, error);
  }
  assertShape(config, key);

  if (memo.size >= MEMO_LIMIT) memo.delete(memo.keys().next().value!);
  memo.set(memoKey, config);
  return config;
}

function assertShape(config: CompiledImageConfig, key: string): void {
  const missing = (
    [
      ["path", typeof config?.path === "string"],
      ["deviceSizes", Array.isArray(config?.deviceSizes)],
      ["imageSizes", Array.isArray(config?.imageSizes)],
      ["formats", Array.isArray(config?.formats)],
      ["domains", Array.isArray(config?.domains)],
      ["remotePatterns", Array.isArray(config?.remotePatterns)],
      ["minimumCacheTTL", typeof config?.minimumCacheTTL === "number"],
      ["maximumResponseBody", typeof config?.maximumResponseBody === "number"],
      ["dangerouslyAllowSVG", typeof config?.dangerouslyAllowSVG === "boolean"],
      ["dangerouslyAllowLocalIP", typeof config?.dangerouslyAllowLocalIP === "boolean"],
      ["contentSecurityPolicy", typeof config?.contentSecurityPolicy === "string"],
      ["contentDispositionType", typeof config?.contentDispositionType === "string"],
    ] as const
  )
    .filter(([, ok]) => !ok)
    .map(([name]) => name);
  if (missing.length > 0) {
    throw new SubstrateError(`image config at ${key} is missing ${missing.join(", ")}`);
  }
}
