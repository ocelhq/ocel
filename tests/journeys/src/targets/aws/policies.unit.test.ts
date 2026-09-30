import { describe, expect, it } from "bun:test";
import {
  deleteEdgePolicy,
  type EdgePolicy,
  listEdgePolicies,
  namespaceOfPolicy,
  orphanedPolicies,
} from "./policies";
import type { Cli } from "./store";

function cache(name: string): EdgePolicy {
  return { kind: "cache-policy", id: `id-${name}`, name };
}

function headers(name: string): EdgePolicy {
  return { kind: "response-headers-policy", id: `id-${name}`, name };
}

describe("namespaceOfPolicy", () => {
  it("reads the namespace a bootstrap named its cache policy after", () => {
    expect(namespaceOfPolicy(cache("j-1799-deploy-next-cloudflare-cache"))).toBe(
      "j-1799-deploy-next-cloudflare",
    );
  });

  it("reads the namespace off a preview tier's policies", () => {
    expect(namespaceOfPolicy(cache("j-1799-deploy-node-cache-preview"))).toBe("j-1799-deploy-node");
    expect(namespaceOfPolicy(headers("j-1799-deploy-node-headers-preview"))).toBe(
      "j-1799-deploy-node",
    );
  });

  it("claims no policy a harness run did not name", () => {
    expect(namespaceOfPolicy(cache("ocel-cache"))).toBeUndefined();
    expect(namespaceOfPolicy(cache("j-1799-deploy-node-images"))).toBeUndefined();
  });

  it("claims no cache policy by a headers policy's name", () => {
    expect(namespaceOfPolicy(cache("j-1799-deploy-node-headers"))).toBeUndefined();
  });
});

describe("orphanedPolicies", () => {
  it("keeps the policies a stack in the account still names, and takes the rest", () => {
    const policies = [
      cache("j-1799-deploy-node-cache"),
      headers("j-1799-deploy-node-headers"),
      cache("j-1800-deploy-next-cloudflare-cache"),
      cache("ocel-cache"),
    ];

    expect(orphanedPolicies(policies, new Set(["j-1799-deploy-node"]))).toEqual([
      cache("j-1800-deploy-next-cloudflare-cache"),
    ]);
  });
});

function scripted(answers: Record<string, string>, calls: string[][]): Cli {
  return async (args) => {
    calls.push(args);
    const answer = answers[args.slice(0, 2).join(" ")];
    if (answer === undefined) {
      throw new Error(`nothing scripted for ${args.join(" ")}`);
    }
    return answer;
  };
}

describe("listEdgePolicies", () => {
  it("lists the custom cache and response headers policies by id and name", async () => {
    const calls: string[][] = [];
    const cli = scripted(
      {
        "cloudfront list-cache-policies": JSON.stringify({
          CachePolicyList: {
            Items: [{ CachePolicy: { Id: "c1", CachePolicyConfig: { Name: "j-1-a-cache" } } }],
          },
        }),
        "cloudfront list-response-headers-policies": JSON.stringify({
          ResponseHeadersPolicyList: {
            Items: [
              {
                ResponseHeadersPolicy: {
                  Id: "h1",
                  ResponseHeadersPolicyConfig: { Name: "j-1-a-headers" },
                },
              },
            ],
          },
        }),
      },
      calls,
    );

    expect(await listEdgePolicies(cli)).toEqual([
      { kind: "cache-policy", id: "c1", name: "j-1-a-cache" },
      { kind: "response-headers-policy", id: "h1", name: "j-1-a-headers" },
    ]);
    expect(calls.every((args) => args.includes("custom"))).toBe(true);
  });
});

describe("deleteEdgePolicy", () => {
  it("deletes the policy under the ETag it reads first", async () => {
    const calls: string[][] = [];
    const cli = scripted(
      { "cloudfront get-cache-policy": "E2ETAG", "cloudfront delete-cache-policy": "" },
      calls,
    );

    await deleteEdgePolicy(cli, cache("j-1-a-cache"));

    expect(calls.at(-1)).toEqual([
      "cloudfront",
      "delete-cache-policy",
      "--id",
      "id-j-1-a-cache",
      "--if-match",
      "E2ETAG",
    ]);
  });
});
