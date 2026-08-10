import type {
  CompiledImageConfig,
  CompiledLocalPattern,
  CompiledRemotePattern,
} from "./contract.mjs";

const REGEXES = new Map<string, RegExp>();

function regex(source: string): RegExp {
  let compiled = REGEXES.get(source);
  if (!compiled) REGEXES.set(source, (compiled = new RegExp(source)));
  return compiled;
}

export function matchLocalPattern(
  pattern: CompiledLocalPattern,
  url: URL,
): boolean {
  if (pattern.search !== undefined && pattern.search !== url.search) {
    return false;
  }
  return regex(pattern.pathname).test(url.pathname);
}

export function matchRemotePattern(
  pattern: CompiledRemotePattern,
  url: URL,
): boolean {
  if (
    pattern.protocol !== undefined &&
    pattern.protocol !== url.protocol.replace(/:$/, "")
  ) {
    return false;
  }
  if (pattern.port !== undefined && pattern.port !== url.port) return false;
  if (!regex(pattern.hostname).test(url.hostname)) return false;
  if (pattern.search !== undefined && pattern.search !== url.search) {
    return false;
  }
  return regex(pattern.pathname).test(url.pathname);
}

export function isAllowedRemote(config: CompiledImageConfig, url: URL): boolean {
  return (
    config.domains.some((domain) => url.hostname === domain) ||
    config.remotePatterns.some((pattern) => matchRemotePattern(pattern, url))
  );
}
