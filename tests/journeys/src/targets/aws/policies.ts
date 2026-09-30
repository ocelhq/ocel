import { HARNESS_PREFIX } from "../../identity";
import type { Cli } from "./store";

export type PolicyKind = "cache-policy" | "response-headers-policy";

export type EdgePolicy = { kind: PolicyKind; id: string; name: string };

const NAME_SUFFIXES: Record<PolicyKind, string[]> = {
  "cache-policy": ["-cache-preview", "-cache"],
  "response-headers-policy": ["-headers-preview", "-headers"],
};

export function namespaceOfPolicy(policy: EdgePolicy): string | undefined {
  if (!policy.name.startsWith(HARNESS_PREFIX)) {
    return undefined;
  }
  const suffix = NAME_SUFFIXES[policy.kind].find((one) => policy.name.endsWith(one));
  return suffix ? policy.name.slice(0, -suffix.length) : undefined;
}

export function orphanedPolicies(policies: EdgePolicy[], namespaces: Set<string>): EdgePolicy[] {
  return policies.filter((policy) => {
    const namespace = namespaceOfPolicy(policy);
    return namespace !== undefined && !namespaces.has(namespace);
  });
}

type CachePolicyPage = {
  CachePolicyList?: {
    Items?: Array<{ CachePolicy?: { Id?: string; CachePolicyConfig?: { Name?: string } } }>;
  };
};

type HeadersPolicyPage = {
  ResponseHeadersPolicyList?: {
    Items?: Array<{
      ResponseHeadersPolicy?: { Id?: string; ResponseHeadersPolicyConfig?: { Name?: string } };
    }>;
  };
};

export async function listEdgePolicies(cli: Cli): Promise<EdgePolicy[]> {
  const found: EdgePolicy[] = [];
  const cache = JSON.parse(
    await cli(["cloudfront", "list-cache-policies", "--type", "custom", "--output", "json"]),
  ) as CachePolicyPage;
  for (const item of cache.CachePolicyList?.Items ?? []) {
    const id = item.CachePolicy?.Id;
    const name = item.CachePolicy?.CachePolicyConfig?.Name;
    if (id && name) {
      found.push({ kind: "cache-policy", id, name });
    }
  }
  const headers = JSON.parse(
    await cli([
      "cloudfront",
      "list-response-headers-policies",
      "--type",
      "custom",
      "--output",
      "json",
    ]),
  ) as HeadersPolicyPage;
  for (const item of headers.ResponseHeadersPolicyList?.Items ?? []) {
    const id = item.ResponseHeadersPolicy?.Id;
    const name = item.ResponseHeadersPolicy?.ResponseHeadersPolicyConfig?.Name;
    if (id && name) {
      found.push({ kind: "response-headers-policy", id, name });
    }
  }
  return found;
}

export async function deleteEdgePolicy(cli: Cli, policy: EdgePolicy): Promise<void> {
  const etag = await cli([
    "cloudfront",
    `get-${policy.kind}`,
    "--id",
    policy.id,
    "--query",
    "ETag",
    "--output",
    "text",
  ]);
  await cli(["cloudfront", `delete-${policy.kind}`, "--id", policy.id, "--if-match", etag]);
}
