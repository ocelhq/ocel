import { describe, expect, it } from "vitest";

import { domainApp } from "../src/domains";

const BOUND = JSON.stringify({ "shop.example": "web", "admin.shop.example": "admin" });

describe("domainApp", () => {
  it("names the app a bound hostname belongs to", () => {
    expect(domainApp(BOUND, "admin.shop.example")).toBe("admin");
  });

  it("matches the hostname whatever case it arrives in", () => {
    expect(domainApp(JSON.stringify({ "Shop.Example": "web" }), "SHOP.example")).toBe("web");
  });

  it("ignores the port the request carries", () => {
    expect(domainApp(BOUND, "shop.example:8787")).toBe("web");
  });

  it("names no app for a hostname nothing binds", () => {
    expect(domainApp(BOUND, "other.example")).toBeUndefined();
  });

  it("names no app when nothing is bound", () => {
    expect(domainApp(undefined, "shop.example")).toBeUndefined();
    expect(domainApp("", "shop.example")).toBeUndefined();
  });

  it("names no app when the binding is not a map of names", () => {
    expect(domainApp("{not json", "shop.example")).toBeUndefined();
    expect(domainApp('["shop.example"]', "shop.example")).toBeUndefined();
    expect(domainApp(JSON.stringify({ "shop.example": 7 }), "shop.example")).toBeUndefined();
    expect(domainApp(JSON.stringify({ "shop.example": "" }), "shop.example")).toBeUndefined();
  });
});
