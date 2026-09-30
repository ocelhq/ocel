import { describe, expect, it } from "vitest";

import { findPreviewTarget, normalizeBaseDomain } from "../src/preview";

const KEY = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0";
const PROJECT_LABEL = "pr-12-web-abcdefghijklmnopp3347l26";
const GLOBAL_LABEL = "shop-abcdefghijklmnoproheemcq";
const BASE = "preview.ocel.app";

const project = { baseDomain: "myapp.com", key: KEY, slug: "p1" };
const global = { baseDomain: BASE, key: KEY };

describe("findPreviewTarget on a project's own preview domain", () => {
  it("accepts the label pkg/edge signs, keyed by the full label under the baked-in slug", async () => {
    expect(await findPreviewTarget(`${PROJECT_LABEL}.myapp.com`, project)).toEqual({
      slug: "p1",
      label: PROJECT_LABEL,
    });
  });

  it("refuses a label whose mac does not match", async () => {
    const forged = `${PROJECT_LABEL.slice(0, -1)}a`;
    expect(await findPreviewTarget(`${forged}.myapp.com`, project)).toBeNull();
  });

  it("refuses a label whose cosmetic prefix was swapped", async () => {
    const swapped = PROJECT_LABEL.replace("pr-12", "pr-13");
    expect(await findPreviewTarget(`${swapped}.myapp.com`, project)).toBeNull();
  });

  it("refuses every label under a different key", async () => {
    expect(
      await findPreviewTarget(`${PROJECT_LABEL}.myapp.com`, { ...project, key: "other-key" }),
    ).toBeNull();
  });

  it("refuses every label when no key is configured", async () => {
    expect(
      await findPreviewTarget(`${PROJECT_LABEL}.myapp.com`, { ...project, key: undefined }),
    ).toBeNull();
  });

  it("refuses a label too short to carry a token and a mac", async () => {
    expect(await findPreviewTarget("pr-42.myapp.com", project)).toBeNull();
  });

  it("lowercases the host and ignores the port", async () => {
    expect(
      await findPreviewTarget(`${PROJECT_LABEL.toUpperCase()}.MyApp.com:8787`, project),
    ).toEqual({
      slug: "p1",
      label: PROJECT_LABEL,
    });
  });

  it("returns null off the base domain, on the bare base and on deeper labels", async () => {
    expect(await findPreviewTarget(`${PROJECT_LABEL}.other.com`, project)).toBeNull();
    expect(await findPreviewTarget("myapp.com", project)).toBeNull();
    expect(await findPreviewTarget(`myapp.com.evil.com`, project)).toBeNull();
    expect(await findPreviewTarget(`a.${PROJECT_LABEL}.myapp.com`, project)).toBeNull();
  });

  it("returns null when the base domain is empty", async () => {
    expect(
      await findPreviewTarget(`${PROJECT_LABEL}.myapp.com`, { ...project, baseDomain: "" }),
    ).toBeNull();
  });
});

describe("findPreviewTarget on the global preview wildcard", () => {
  it("reads the slug as everything before the label's last hyphen", async () => {
    expect(await findPreviewTarget(`${GLOBAL_LABEL}.${BASE}`, global)).toEqual({
      slug: "shop",
      label: GLOBAL_LABEL,
    });
  });

  it("keeps a slug that itself contains hyphens", async () => {
    expect(await findPreviewTarget(`${PROJECT_LABEL}.${BASE}`, global)).toEqual({
      slug: "pr-12-web",
      label: PROJECT_LABEL,
    });
  });

  it("refuses a forged label before naming any slug", async () => {
    expect(await findPreviewTarget(`shop-abcdefghijklmnopaaaaaaaa.${BASE}`, global)).toBeNull();
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
