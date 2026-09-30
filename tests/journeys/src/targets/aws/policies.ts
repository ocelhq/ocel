import { HARNESS_PREFIX } from "../../identity";
import { runIdOf } from "./runs";
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
    return (
      namespace !== undefined && runIdOf(policy.name) !== undefined && !namespaces.has(namespace)
    );
  });
}

type PolicyItems = { Items?: unknown[]; NextMarker?: string };

type CachePolicyItem = { CachePolicy?: { Id?: string; CachePolicyConfig?: { Name?: string } } };

type HeadersPolicyItem = {
  ResponseHeadersPolicy?: { Id?: string; ResponseHeadersPolicyConfig?: { Name?: string } };
};

async function listPages(cli: Cli, command: string, listKey: string): Promise<unknown[]> {
  const items: unknown[] = [];
  let marker = "";
  do {
    const raw = await cli([
      "cloudfront",
      command,
      "--type",
      "custom",
      ...(marker ? ["--marker", marker] : []),
      "--output",
      "json",
    ]);
    const list = (JSON.parse(raw) as Record<string, PolicyItems | undefined>)[listKey];
    items.push(...(list?.Items ?? []));
    marker = list?.NextMarker ?? "";
  } while (marker);
  return items;
}

export async function listEdgePolicies(cli: Cli): Promise<EdgePolicy[]> {
  const found: EdgePolicy[] = [];
  for (const item of (await listPages(
    cli,
    "list-cache-policies",
    "CachePolicyList",
  )) as CachePolicyItem[]) {
    const id = item.CachePolicy?.Id;
    const name = item.CachePolicy?.CachePolicyConfig?.Name;
    if (id && name) {
      found.push({ kind: "cache-policy", id, name });
    }
  }
  for (const item of (await listPages(
    cli,
    "list-response-headers-policies",
    "ResponseHeadersPolicyList",
  )) as HeadersPolicyItem[]) {
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
