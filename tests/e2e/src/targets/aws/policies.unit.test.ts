import { describe, expect, it } from "bun:test";
import {
  deleteEdgePolicy,
  type EdgePolicy,
  listEdgePolicies,
  namespaceFieldOfPolicy,
  orphanedPolicies,
} from "./policies";
import type { Cli } from "./store";

function cache(name: string): EdgePolicy {
  return { kind: "cache-policy", id: `id-${name}`, name };
}

function headers(name: string): EdgePolicy {
  return { kind: "response-headers-policy", id: `id-${name}`, name };
}

describe("namespaceFieldOfPolicy", () => {
  it("reads the namespace field a bootstrap named its cache policy after", () => {
    expect(namespaceFieldOfPolicy(cache("j-179-c00ae4-prod-cache"))).toBe("j-179-c00ae4");
  });

  it("reads the namespace field off a preview tier's policies", () => {
    expect(namespaceFieldOfPolicy(cache("j-187-e5b227-preview-cache"))).toBe("j-187-e5b227");
    expect(namespaceFieldOfPolicy(headers("j-187-e5b227-preview-headers"))).toBe("j-187-e5b227");
  });

  it("claims no policy a harness run did not name", () => {
    expect(namespaceFieldOfPolicy(cache("ocel-prod-cache"))).toBeUndefined();
    expect(namespaceFieldOfPolicy(cache("j-187-e5b227-prod-images"))).toBeUndefined();
  });

  it("claims no cache policy by a headers policy's name", () => {
    expect(namespaceFieldOfPolicy(cache("j-187-e5b227-prod-headers"))).toBeUndefined();
  });
});

describe("orphanedPolicies", () => {
  it("keeps the policies a stack in the account still names, and takes the rest", () => {
    const policies = [
      cache("j-187-e5b227-prod-cache"),
      headers("j-187-e5b227-prod-headers"),
      cache("j-179-c00ae4-prod-cache"),
      cache("ocel-prod-cache"),
    ];

    expect(orphanedPolicies(policies, new Set(["j-1874-deploy-node"]))).toEqual([
      cache("j-179-c00ae4-prod-cache"),
    ]);
  });

  it("keeps a local run's policy, since nothing can tell whether that run is still going", () => {
    expect(orphanedPolicies([cache("j-loc-3f13ed-prod-cache")], new Set())).toEqual([]);
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
            Items: [{ CachePolicy: { Id: "c1", CachePolicyConfig: { Name: "j-1-a-prod-cache" } } }],
          },
        }),
        "cloudfront list-response-headers-policies": JSON.stringify({
          ResponseHeadersPolicyList: {
            Items: [
              {
                ResponseHeadersPolicy: {
                  Id: "h1",
                  ResponseHeadersPolicyConfig: { Name: "j-1-a-prod-headers" },
                },
              },
            ],
          },
        }),
      },
      calls,
    );

    expect(await listEdgePolicies(cli)).toEqual([
      { kind: "cache-policy", id: "c1", name: "j-1-a-prod-cache" },
      { kind: "response-headers-policy", id: "h1", name: "j-1-a-prod-headers" },
    ]);
    expect(calls.every((args) => args.includes("custom"))).toBe(true);
  });
});

describe("listEdgePolicies across pages", () => {
  it("follows NextMarker until the listing ends", async () => {
    const calls: string[][] = [];
    const cli: Cli = async (args) => {
      calls.push(args);
      if (args[1] === "list-response-headers-policies") {
        return JSON.stringify({ ResponseHeadersPolicyList: { Items: [] } });
      }
      const second = args.includes("--marker");
      return JSON.stringify({
        CachePolicyList: {
          Items: [
            {
              CachePolicy: {
                Id: second ? "c2" : "c1",
                CachePolicyConfig: { Name: second ? "j-1-b-prod-cache" : "j-1-a-prod-cache" },
              },
            },
          ],
          ...(second ? {} : { NextMarker: "page-2" }),
        },
      });
    };

    expect((await listEdgePolicies(cli)).map((policy) => policy.id)).toEqual(["c1", "c2"]);
    expect(calls[1]).toContain("page-2");
  });
});

describe("deleteEdgePolicy", () => {
  it("deletes the policy under the ETag it reads first", async () => {
    const calls: string[][] = [];
    const cli = scripted(
      { "cloudfront get-cache-policy": "E2ETAG", "cloudfront delete-cache-policy": "" },
      calls,
    );

    await deleteEdgePolicy(cli, cache("j-1-a-prod-cache"));

    expect(calls.at(-1)).toEqual([
      "cloudfront",
      "delete-cache-policy",
      "--id",
      "id-j-1-a-prod-cache",
      "--if-match",
      "E2ETAG",
    ]);
  });
});
