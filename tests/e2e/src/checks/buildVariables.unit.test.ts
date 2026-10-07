import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  BUILD_SECRET_VALUE,
  BUILD_SENSITIVE_VALUE,
  buildVariablesChecks,
  setsBuildVariables,
} from "./buildVariables";
import type { Check, CheckContext, Fetch } from "./context";
import { envChecks } from "./env";

const BASE = "https://web-j-1-build-variables-next.journey.test";

function checkTitled(prefix: string): Check {
  const found = buildVariablesChecks.find((one) => one.title.startsWith(prefix));
  if (!found) {
    throw new Error(`no build-variables check starting ${prefix}`);
  }
  return found;
}

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

let root: string;
let projectDir: string;
let tempDir: string;

beforeEach(async () => {
  root = await mkdtemp(path.join(tmpdir(), "build-variables-check-"));
  projectDir = path.join(root, "project");
  tempDir = path.join(root, "tmp");
  await mkdir(path.join(projectDir, ".ocel", "output", "functions", "web"), { recursive: true });
  await mkdir(tempDir, { recursive: true });
});

afterEach(async () => {
  await rm(root, { recursive: true, force: true });
});

function context(fetch: Fetch = async () => new Response(null, { status: 404 })): CheckContext {
  return {
    app: "web",
    baseUrl: BASE,
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "verify",
    notes: new Map(),
    fetch,
    reach: async (url) => url,
    readExposed: async () => "",
    journeyNonce: "journey-nonce",
    projectDir,
    tempDir,
  };
}

describe("the build-variables checks", () => {
  it("are what makes a target set the sensitive and the secret value before it deploys", () => {
    expect(setsBuildVariables(buildVariablesChecks)).toBe(true);
    expect(setsBuildVariables(envChecks)).toBe(false);
  });

  describe("the route that read both values at import", () => {
    const check = () => checkTitled("GET /values/<slug>");

    it("passes when it answers the digest of each value set", async () => {
      const fetch: Fetch = async () =>
        Response.json({
          slug: "journey",
          sensitive: sha256(BUILD_SENSITIVE_VALUE),
          secret: sha256(BUILD_SECRET_VALUE),
        });
      await check().run(context(fetch));
    });

    it("fails when the secret it read is not the one set", async () => {
      const fetch: Fetch = async () =>
        Response.json({ slug: "journey", sensitive: sha256(BUILD_SENSITIVE_VALUE), secret: "" });
      await expect(check().run(context(fetch))).rejects.toThrow();
    });
  });

  describe("the build output", () => {
    const check = () => checkTitled("no file under .ocel/output");

    it("passes when no file holds either value", async () => {
      await writeFile(
        path.join(projectDir, ".ocel", "output", "functions", "web", "index.mjs"),
        "x",
      );
      await check().run(context());
    });

    it("fails naming the file that holds the secret", async () => {
      const leaked = path.join(projectDir, ".ocel", "output", "functions", "web", "page.html");
      await writeFile(leaked, `<p>${BUILD_SECRET_VALUE}</p>`);
      await expect(check().run(context())).rejects.toThrow(/functions\/web\/page\.html/);
    });

    it("fails when the build left no output to look through", async () => {
      await rm(path.join(projectDir, ".ocel"), { recursive: true });
      await expect(check().run(context())).rejects.toThrow(/\.ocel\/output/);
    });
  });

  describe("the live dirs", () => {
    const check = () => checkTitled("no ocel-live-* dir");

    it("passes when another build's live dir holds other values", async () => {
      await mkdir(path.join(tempDir, "ocel-live-other"));
      await writeFile(path.join(tempDir, "ocel-live-other", "SECRET_TOKEN"), "someone else's");
      await check().run(context());
    });

    it("fails naming a live dir left holding the sensitive value", async () => {
      await mkdir(path.join(tempDir, "ocel-live-123"));
      await writeFile(
        path.join(tempDir, "ocel-live-123", "BUILD_SENSITIVE"),
        BUILD_SENSITIVE_VALUE,
      );
      await expect(check().run(context())).rejects.toThrow(/ocel-live-123/);
    });
  });
});
