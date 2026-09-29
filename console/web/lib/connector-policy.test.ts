import { describe, expect, it } from "vitest";
import { abilityOf, scopesFor } from "./connector-policy";

const everything = ["variables.read", "variables.write", "variables.reveal"];

describe("scopesFor", () => {
  it("gives an owner read, write and reveal", () => {
    expect(scopesFor("owner", everything)).toEqual([
      "variables.read",
      "variables.write",
      "variables.reveal",
    ]);
  });

  it("stops an admin short of reveal", () => {
    expect(scopesFor("admin", everything)).toEqual(["variables.read", "variables.write"]);
  });

  it("leaves a member reading", () => {
    expect(scopesFor("member", everything)).toEqual(["variables.read"]);
  });

  it("gives an unknown role nothing", () => {
    expect(scopesFor("visitor", everything)).toEqual([]);
  });

  it("unions the roles a member has at once", () => {
    expect(scopesFor("member,admin", everything)).toEqual(["variables.read", "variables.write"]);
  });

  it("drops what the connector does not answer", () => {
    expect(scopesFor("owner", ["variables.read"])).toEqual(["variables.read"]);
  });
});

describe("abilityOf", () => {
  it("lets an owner write and reveal", () => {
    expect(abilityOf(scopesFor("owner", everything))).toEqual({ write: true, reveal: true });
  });

  it("lets an admin write but not reveal", () => {
    expect(abilityOf(scopesFor("admin", everything))).toEqual({ write: true, reveal: false });
  });

  it("lets a member do neither", () => {
    expect(abilityOf(scopesFor("member", everything))).toEqual({ write: false, reveal: false });
  });

  it("takes nothing the connector does not advertise", () => {
    expect(abilityOf(scopesFor("owner", ["variables.read"]))).toEqual({
      write: false,
      reveal: false,
    });
  });
});
