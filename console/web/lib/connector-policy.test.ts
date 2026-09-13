import { describe, expect, it } from "vitest";
import { scopesFor } from "./connector-policy";

const everything = ["envvars.read", "envvars.write", "envvars.reveal"];

describe("scopesFor", () => {
  it("gives an owner read, write and reveal", () => {
    expect(scopesFor("owner", everything)).toEqual([
      "envvars.read",
      "envvars.write",
      "envvars.reveal",
    ]);
  });

  it("stops an admin short of reveal", () => {
    expect(scopesFor("admin", everything)).toEqual(["envvars.read", "envvars.write"]);
  });

  it("leaves a member reading", () => {
    expect(scopesFor("member", everything)).toEqual(["envvars.read"]);
  });

  it("gives an unknown role nothing", () => {
    expect(scopesFor("visitor", everything)).toEqual([]);
  });

  it("unions the roles a member holds at once", () => {
    expect(scopesFor("member,admin", everything)).toEqual(["envvars.read", "envvars.write"]);
  });

  it("drops what the connector does not answer", () => {
    expect(scopesFor("owner", ["envvars.read"])).toEqual(["envvars.read"]);
  });
});
