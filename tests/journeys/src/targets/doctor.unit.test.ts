import { describe, expect, it } from "bun:test";
import { bootstrappedMissed, unbootstrappedMissed } from "./doctor";

const PROJECT = [
  "Project  ocel-journey-bootstrap · ocel.json",
  "  ✓ config loads — 0 apps",
  "  ✓ provider vps dev",
  "",
  "VPS  root@10.0.0.7",
  "  ✓ credentials valid",
].join("\n");

const PREVIEW = [
  "Preview",
  "  ⚠ no preview domain",
  "    → run `ocel domain use '*.preview.example.com' --preview`",
  "  – not set up — run `ocel bootstrap preview` to add previews",
].join("\n");

const FRESH = [
  PROJECT,
  "",
  "Production",
  "  – not set up — run `ocel bootstrap production` to set it up",
  "",
  PREVIEW,
  "",
  "1 warning.",
  "",
].join("\n");

const HOST_CHECKS = [
  "Host checks",
  "  ✓ port 80 answers from this machine (not proof the internet reaches it)",
  "  ✓ nothing listens on tcp 2019 inside ocel-proxy; admin is on /run/caddy-admin.sock only",
  "  ✓ ocel-switchboard is running and answers over its control socket in /run/ocel/switchboard",
].join("\n");

const BOOTSTRAPPED = [
  PROJECT,
  "",
  "Production",
  "  ✓ bootstrapped, current",
  "",
  PREVIEW,
  "",
  HOST_CHECKS,
  "",
  "1 warning.",
  "",
].join("\n");

describe("unbootstrappedMissed", () => {
  it("passes a doctor that calls production not set up", () => {
    expect(unbootstrappedMissed(FRESH)).toBeUndefined();
  });

  it("reads the verdict through the colour a terminal paints it in", () => {
    const painted = FRESH.replace("  – not set up", "  \u001b[2m– not set up").replace(
      "to set it up\n",
      "to set it up\u001b[0m\n",
    );
    expect(unbootstrappedMissed(painted)).toBeUndefined();
  });

  it("does not read preview's absence as production's", () => {
    expect(unbootstrappedMissed(BOOTSTRAPPED)).toContain("production is not set up");
  });
});

describe("bootstrappedMissed", () => {
  it("passes a current production with every check of the proxy ocel runs passing", () => {
    expect(bootstrappedMissed(BOOTSTRAPPED, true)).toBeUndefined();
  });

  it("names a production doctor still calls unbootstrapped", () => {
    expect(bootstrappedMissed(FRESH, true)).toContain("never calls production bootstrapped");
  });

  it("names a doctor that printed no host checks at all", () => {
    const silent = BOOTSTRAPPED.replace(`${HOST_CHECKS}\n\n`, "");
    expect(bootstrappedMissed(silent, false)).toContain("no host checks section");
  });

  it("names a refused check", () => {
    const refused = BOOTSTRAPPED.replace(
      "  ✓ nothing listens on tcp 2019 inside ocel-proxy; admin is on /run/caddy-admin.sock only",
      "  ✗ *:2019 listens on 2019 inside ocel-proxy: the admin api is open to anything that reaches the proxy",
    );
    const missed = bootstrappedMissed(refused, true);
    expect(missed).toContain("refused something");
    expect(missed).toContain("nothing listens on tcp 2019 inside ocel-proxy");
  });

  it("asks nothing of the proxy's own ports on a box whose front proxy owns them", () => {
    const fronted = BOOTSTRAPPED.replace(
      "  ✓ port 80 answers from this machine (not proof the internet reaches it)\n",
      "",
    ).replace(
      "  ✓ nothing listens on tcp 2019 inside ocel-proxy; admin is on /run/caddy-admin.sock only",
      "  ✓ nginx listens on :80 and :443",
    );
    expect(bootstrappedMissed(fronted, false)).toBeUndefined();
    expect(bootstrappedMissed(fronted, true)).toContain("port 80 answers from this machine");
  });

  it("names a certificates section over a box that serves no hostname", () => {
    const certified = BOOTSTRAPPED.replace(
      "1 warning.",
      "Certificates\n  ✓ web.localhost — no expiry reported, the proxy renews it over http-01\n\n1 warning.",
    );
    expect(bootstrappedMissed(certified, true)).toContain("certificates section");
  });
});
