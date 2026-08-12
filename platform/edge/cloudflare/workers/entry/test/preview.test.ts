import { describe, expect, it } from "vitest";

import {
  globalPreviewTarget,
  normalizeBaseDomain,
  previewApps,
  previewTarget,
} from "../src/preview";

const SOLE = ["web"];
const MANY = ["web", "admin"];

describe("previewTarget", () => {
  it("reads a bare label as the pointer of the project's sole app", () => {
    expect(previewTarget("flaky-web-2626.myapp.com", "myapp.com", SOLE)).toEqual({
      pointer: "flaky-web-2626",
      app: "web",
    });
  });

  it("refuses a bare label when the project has more than one app", () => {
    expect(previewTarget("flaky-web-2626.myapp.com", "myapp.com", MANY)).toBeNull();
  });

  it("splits <pointer>--<app> against the project's apps", () => {
    expect(previewTarget("pr-42--admin.myapp.com", "myapp.com", MANY)).toEqual({
      pointer: "pr-42",
      app: "admin",
    });
  });

  it("accepts the explicit form in a single-app project too", () => {
    expect(previewTarget("pr-42--web.myapp.com", "myapp.com", SOLE)).toEqual({
      pointer: "pr-42",
      app: "web",
    });
  });

  it("keeps a pointer that itself contains the separator", () => {
    expect(previewTarget("a--b--admin.myapp.com", "myapp.com", MANY)).toEqual({
      pointer: "a--b",
      app: "admin",
    });
    expect(previewTarget("a--b.myapp.com", "myapp.com", SOLE)).toEqual({
      pointer: "a--b",
      app: "web",
    });
  });

  it("matches an app name containing hyphens", () => {
    expect(
      previewTarget("pr-42--admin-ui.myapp.com", "myapp.com", ["web", "admin-ui"]),
    ).toEqual({ pointer: "pr-42", app: "admin-ui" });
  });

  it("prefers the longest app name when two could match", () => {
    expect(previewTarget("p--a--b.myapp.com", "myapp.com", ["b", "a--b"])).toEqual({
      pointer: "p",
      app: "a--b",
    });
  });

  it("returns null for an app the project does not have", () => {
    expect(previewTarget("pr-42--nope.myapp.com", "myapp.com", MANY)).toBeNull();
  });

  it("reads an unknown app suffix as part of the pointer for a sole app", () => {
    expect(previewTarget("pr-42--nope.myapp.com", "myapp.com", SOLE)).toEqual({
      pointer: "pr-42--nope",
      app: "web",
    });
  });

  it("returns null when the separator leaves no pointer", () => {
    expect(previewTarget("--admin.myapp.com", "myapp.com", MANY)).toBeNull();
  });

  it("lowercases the host and ignores the port", () => {
    expect(
      previewTarget("PR-42--Admin.MyApp.com:8787", "myapp.com", MANY),
    ).toEqual({ pointer: "pr-42", app: "admin" });
  });

  it("returns null for the apex (no subdomain label)", () => {
    expect(previewTarget("myapp.com", "myapp.com", SOLE)).toBeNull();
  });

  it("returns null for a host not under the base domain", () => {
    expect(previewTarget("pr-42--web.other.com", "myapp.com", SOLE)).toBeNull();
    expect(previewTarget("myapp.com.evil.com", "myapp.com", SOLE)).toBeNull();
  });

  it("returns null for a multi-label subdomain the wildcard route cannot match", () => {
    expect(previewTarget("a.b.myapp.com", "myapp.com", SOLE)).toBeNull();
  });

  it("returns null when the label is empty", () => {
    expect(previewTarget(".myapp.com", "myapp.com", SOLE)).toBeNull();
  });

  it("tolerates a base domain given with surrounding dots", () => {
    expect(previewTarget("pr-42.myapp.com", ".myapp.com.", SOLE)).toEqual({
      pointer: "pr-42",
      app: "web",
    });
  });

  it("returns null when the base domain is empty", () => {
    expect(previewTarget("pr-42.myapp.com", "", SOLE)).toBeNull();
  });

  it("returns null when the project declares no apps", () => {
    expect(previewTarget("pr-42.myapp.com", "myapp.com", [])).toBeNull();
    expect(previewTarget("pr-42--web.myapp.com", "myapp.com", [])).toBeNull();
  });
});

describe("globalPreviewTarget", () => {
  const BASE = "preview.ocel.sh";

  it("reads slug--pointer--app positionally", () => {
    expect(globalPreviewTarget("acme--pr-42--admin." + BASE, BASE)).toEqual({
      slug: "acme",
      pointer: "pr-42",
      app: "admin",
    });
  });

  it("leaves the app unset when the label carries only slug and pointer", () => {
    expect(globalPreviewTarget("acme--pr-42." + BASE, BASE)).toEqual({
      slug: "acme",
      pointer: "pr-42",
    });
  });

  it("keeps an app name that itself contains the separator", () => {
    expect(globalPreviewTarget("acme--pr-42--a--b." + BASE, BASE)).toEqual({
      slug: "acme",
      pointer: "pr-42",
      app: "a--b",
    });
  });

  it("returns null for a single token", () => {
    expect(globalPreviewTarget("acme." + BASE, BASE)).toBeNull();
  });

  it("returns null when any token is empty", () => {
    expect(globalPreviewTarget("--pr-42." + BASE, BASE)).toBeNull();
    expect(globalPreviewTarget("acme--." + BASE, BASE)).toBeNull();
    expect(globalPreviewTarget("acme--pr-42--." + BASE, BASE)).toBeNull();
    expect(globalPreviewTarget("acme----admin." + BASE, BASE)).toBeNull();
  });

  it("lowercases and ignores the port", () => {
    expect(globalPreviewTarget("Acme--PR-42--Admin." + BASE + ":8787", BASE)).toEqual({
      slug: "acme",
      pointer: "pr-42",
      app: "admin",
    });
  });

  it("returns null off the base domain, on the bare base, and on deeper labels", () => {
    expect(globalPreviewTarget("acme--pr-42.other.com", BASE)).toBeNull();
    expect(globalPreviewTarget(BASE, BASE)).toBeNull();
    expect(globalPreviewTarget(BASE + ".evil.com", BASE)).toBeNull();
    expect(globalPreviewTarget("a.acme--pr-42." + BASE, BASE)).toBeNull();
  });

  it("returns null when the base domain is empty", () => {
    expect(globalPreviewTarget("acme--pr-42.preview.ocel.sh", "")).toBeNull();
  });

  it("tolerates surrounding dots on the base domain", () => {
    expect(globalPreviewTarget("acme--pr-42." + BASE, "." + BASE + ".")).toEqual({
      slug: "acme",
      pointer: "pr-42",
    });
  });
});

describe("previewApps", () => {
  it("splits a comma-separated list, lowercased and trimmed", () => {
    expect(previewApps(" Web , Admin-UI ")).toEqual(["web", "admin-ui"]);
  });

  it("drops empty entries so a trailing comma never fakes a second app", () => {
    expect(previewApps("web,")).toEqual(["web"]);
    expect(previewApps(",,")).toEqual([]);
  });

  it("treats undefined and empty as no apps", () => {
    expect(previewApps(undefined)).toEqual([]);
    expect(previewApps("")).toEqual([]);
  });
});

describe("normalizeBaseDomain", () => {
  it("lowercases and strips surrounding dots", () => {
    expect(normalizeBaseDomain(".MyApp.com.")).toBe("myapp.com");
  });

  it("treats undefined, empty, and dots-only as no base domain", () => {
    expect(normalizeBaseDomain(undefined)).toBe("");
    expect(normalizeBaseDomain("")).toBe("");
    expect(normalizeBaseDomain(".")).toBe("");
    expect(normalizeBaseDomain("...")).toBe("");
  });
});
