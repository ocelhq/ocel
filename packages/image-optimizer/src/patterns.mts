import type {
  CompiledImageConfig,
  CompiledLocalPattern,
  CompiledRemotePattern,
} from "./contract.mjs";

// The compiled patterns are per-config, and a warm container serves the same
// few configs for its whole life, so their regexes are built once and reused.
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

// Matched against url.hostname, never url.host: the compiled hostname regex is
// anchored and knows nothing of ports or userinfo, so a pattern that should
// match "cdn.example.com:8443" would fail against `host`, and `hostname` is
// also what strips the userinfo an attacker would otherwise use to hang an
// allowlisted name off a host they control
// (https://cdn.example.com@evil.example/).
export function matchRemotePattern(
  pattern: CompiledRemotePattern,
  url: URL,
): boolean {
  // The pattern side arrives with its trailing colon already stripped.
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
