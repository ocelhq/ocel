import { createExecutionContext } from "cloudflare:test";
import { CLIENT_ADDRESS_HEADER } from "@platform/edge-contract/client-address";
import { describe, expect, it } from "vitest";
import { withClientAddress } from "../src/client-address";
import type { DeploymentsBinding } from "../src/deployments";
import worker, { type Env } from "../src/index";
import { capturing, makeRecord, withGlobalFetch } from "./origin-deps";

const binding: DeploymentsBinding = {
  async pointerRecord() {
    const record = makeRecord();
    return { kind: "record", identity: record.identity, record };
  },
};

const env: Env = {
  DEPLOYMENTS: binding,
  OCEL_SLUG: "p1",
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
};

async function carried(headers: Record<string, string>): Promise<Headers> {
  const wire = capturing();
  await withGlobalFetch(wire.fetch, () =>
    worker.fetch(
      new Request("https://app.example.com/users", { headers }),
      env,
      createExecutionContext(),
    ),
  );
  return wire.calls[0]!.headers;
}

describe("the client's address", () => {
  it("reaches the origin in the carrier, whatever chain the client forged", async () => {
    const sent = await carried({ "cf-connecting-ip": "203.0.113.9", "x-forwarded-for": "6.6.6.6" });
    expect(sent.get(CLIENT_ADDRESS_HEADER)).toBe("203.0.113.9");
  });

  it("drops a carrier the client sent", async () => {
    const sent = await carried({ [CLIENT_ADDRESS_HEADER]: "6.6.6.6" });
    expect(sent.get(CLIENT_ADDRESS_HEADER)).toBeNull();
  });

  it("keeps the request's cf properties", () => {
    const request = new Request("https://app.example.com/", {
      headers: { "cf-connecting-ip": "203.0.113.9" },
      cf: { country: "ZA" },
    });

    expect(withClientAddress(request).cf?.country).toBe("ZA");
  });
});
