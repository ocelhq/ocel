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
  it("refuses nothing when no retiring part and no trust store object is left", () => {
    expect(refuseWhatDestroyLeft("j-1-deploy-go", [], [])).toBeUndefined();
  });

  it("names each part left in the tier the destroyed project was the only user of", () => {
    expect(refuseWhatDestroyLeft("j-1-deploy-go", ["containers"], [])?.message).toBe(
      "ocel projects destroy removed j-1-deploy-go, its namespace's only project, and left its production tier holding containers, which retires with its last user",
    );
  });

  it("names each object left that the load balancer's trust store is kept with", () => {
    expect(
      refuseWhatDestroyLeft("j-1-deploy-go", [], ["trust/client-cas.json", "trust/client-cas.pem"])
        ?.message,
    ).toBe(
      "ocel projects destroy removed j-1-deploy-go, its namespace's only project, and left its production tier holding trust/client-cas.json and trust/client-cas.pem, kept for the load balancer's trust store",
    );
  });

  it("names a lease left on the trust store without calling it a client CA", () => {
    expect(
      refuseWhatDestroyLeft("j-1-deploy-go", [], ["leases/trust/client-cas.lease"])?.message,
    ).toBe(
      "ocel projects destroy removed j-1-deploy-go, its namespace's only project, and left its production tier holding leases/trust/client-cas.lease, kept for the load balancer's trust store",
    );
  });

  it("names both when parts and trust store objects are left", () => {
    expect(
      refuseWhatDestroyLeft(
        "j-1-deploy-go",
        ["containers", "function-network"],
        ["trust/client-cas.json"],
      )?.message,
    ).toBe(
      "ocel projects destroy removed j-1-deploy-go, its namespace's only project, and left its production tier holding containers and function-network, which retire with their last user, and trust/client-cas.json, kept for the load balancer's trust store",
    );
  });
});
