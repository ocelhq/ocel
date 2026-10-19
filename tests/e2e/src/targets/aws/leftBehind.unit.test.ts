import { describe, expect, it } from "bun:test";
import { readRetiringParts, refuseWhatDestroyLeft } from "./leftBehind";

const part = (name: string, retiresWithLastUser: boolean) => ({
  name,
  usedBy: [],
  retiresWithLastUser,
});

const status = (tiers: unknown[]) => JSON.stringify({ ok: true, data: { tiers } });

describe("readRetiringParts", () => {
  it("names the production parts that retire with their last user, and no other", () => {
    const stdout = status([
      {
        tier: "TIER_PRODUCTION",
        present: true,
        parts: [part("base", false), part("containers", true), part("function-network", true)],
      },
      { tier: "TIER_PREVIEW", present: true, parts: [part("containers", true)] },
    ]);

    expect(readRetiringParts(stdout)).toEqual(["containers", "function-network"]);
  });

  it("names none when the production tier holds only parts that outlive their users", () => {
    expect(
      readRetiringParts(
        status([{ tier: "TIER_PRODUCTION", present: true, parts: [part("base", false)] }]),
      ),
    ).toEqual([]);
  });

  it("reads the result past the lines that are not it", () => {
    const stdout = [
      "reading the production bootstrap",
      JSON.stringify({ event: "span" }),
      status([{ tier: "TIER_PRODUCTION", present: true, parts: [part("containers", true)] }]),
    ].join("\n");

    expect(readRetiringParts(stdout)).toEqual(["containers"]);
  });

  it("refuses output that holds no bootstrap status, naming the command", () => {
    expect(() => readRetiringParts('{"ok":false,"error":{}}\n')).toThrow(
      /`ocel bootstrap status production --json` printed no tiers/,
    );
  });
});

describe("refuseWhatDestroyLeft", () => {
  it("refuses nothing when no retiring part and no client CA object is left", () => {
    expect(refuseWhatDestroyLeft("j-1-deploy-go", [], [])).toBeUndefined();
  });

  it("names each part left in the tier the destroyed project was the only user of", () => {
    expect(refuseWhatDestroyLeft("j-1-deploy-go", ["containers"], [])?.message).toBe(
      "ocel destroy production removed j-1-deploy-go, its namespace's only project, and left its production tier holding containers, which retires with its last user",
    );
  });

  it("names each client CA object left, which a load balancer's trust store is built from", () => {
    expect(
      refuseWhatDestroyLeft(
        "j-1-deploy-go",
        [],
        ["ocel/trust/production/client-cas.json", "ocel/trust/production/client-cas.pem"],
      )?.message,
    ).toBe(
      "ocel destroy production removed j-1-deploy-go, its namespace's only project, and left its production tier holding ocel/trust/production/client-cas.json and ocel/trust/production/client-cas.pem, the client CAs a load balancer's trust store is built from",
    );
  });

  it("names both when parts and client CA objects are left", () => {
    expect(
      refuseWhatDestroyLeft(
        "j-1-deploy-go",
        ["containers", "function-network"],
        ["ocel/trust/production/client-cas.json"],
      )?.message,
    ).toBe(
      "ocel destroy production removed j-1-deploy-go, its namespace's only project, and left its production tier holding containers and function-network, which retire with their last user, and ocel/trust/production/client-cas.json, the client CAs a load balancer's trust store is built from",
    );
  });
});
