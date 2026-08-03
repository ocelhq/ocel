import { createHash } from "node:crypto";
import type { CompiledImageConfig } from "./contract.mjs";
import { SubstrateError } from "./errors.mjs";
import { imageConfigKey, type BuildIdentity } from "./keys.mjs";
import type { ObjectStore } from "./store.mjs";

// Loading the config this function validates against, and refusing to proceed
// on anything it cannot prove is the config the build compiled.
//
// The edge sends a configHash and nothing else about the config. That is the
// whole of the trust model: the worker is told what to ask for, and this side
// decides what that means by fetching the artifact and hashing it. A worker
// that lies about the hash gets a refusal, and a worker running an older
// manifest cannot keep serving under a config the build has since tightened.

// A config is a few kilobytes; a remotePatterns list large enough to approach
// this would already be unservable. The ceiling exists so that a wrong or
// hostile object at this key cannot be a memory exhaustion before it is a hash
// mismatch.
const CONFIG_LIMIT = 1024 * 1024;

// Warm containers hold the few configs their traffic touches. Keyed by hash
// alone, which is sound precisely because the hash is verified: whatever comes
// back hashes to the key it is stored under, so there is no build identity a
// cached entry could be wrong for.
const MEMO_LIMIT = 64;
const memo = new Map<string, CompiledImageConfig>();

export function resetConfigMemo(): void {
  memo.clear();
}

export async function loadImageConfig(
  store: ObjectStore,
  id: BuildIdentity,
  configHash: string,
): Promise<CompiledImageConfig> {
  if (!/^[0-9a-f]{64}$/.test(configHash)) {
    throw new SubstrateError("configHash is not a sha256 digest", configHash);
  }
  const cached = memo.get(configHash);
  if (cached) return cached;

  const key = imageConfigKey(id);
  let object;
  try {
    object = await store.get(key, CONFIG_LIMIT);
  } catch (error) {
    throw new SubstrateError(`reading ${key}`, error);
  }
  if (!object) throw new SubstrateError(`no image config at ${key}`);

  // Hashed over the exact bytes that were uploaded, which is why the adapter
  // hashes the serialized artifact rather than an object it re-serializes:
  // neither side has to reproduce the other's canonicalization, and there is no
  // JSON round trip in between for a difference to hide in.
  const digest = createHash("sha256").update(object.bytes).digest("hex");
  if (digest !== configHash) {
    // No downgrade path, no "close enough", no serving under whatever was
    // found. The config names the hosts this function may fetch from; a config
    // we cannot authenticate is one an attacker may have chosen.
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
  memo.set(configHash, config);
  return config;
}

// The hash proves the bytes are the build's, not that this function's
// expectations of them still hold — an artifact from a future adapter would
// hash correctly and still be missing a field every check below reads. Absent
// fields would otherwise read as undefined and quietly widen the validation
// that is the whole point of loading this.
//
// Every field the pipeline reads is listed, including the two that are only ever
// copied into a response header: `undefined` in a Record<string, string> is
// serialized by the Lambda prelude as the literal string "undefined", so an
// artifact missing contentSecurityPolicy answered 200 with
// `content-security-policy: undefined` — a hash-valid config silently dropping
// the header the SVG bypass depends on. `maximumRedirects` is absent from this
// list because it is read through `?? MAX_REDIRECTS` and so is safe by
// construction.
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
