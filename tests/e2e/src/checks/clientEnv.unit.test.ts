import { describe, expect, it } from "bun:test";
import { createHash } from "node:crypto";
import { buildVariablesChecks } from "./buildVariables";
import {
  CLIENT_ENV_PUBLIC_VALUE,
  CLIENT_ENV_SENSITIVE_VALUE,
  clientEnvDeploymentUrlCheck,
  clientEnvRenderedCheck,
  clientEnvSensitiveUnsentCheck,
  clientEnvValuesOf,
  inlineScriptsIn,
  nextClientEnvChecks,
  nextEdgePublicValueCheck,
  nextPublicValueInlinedCheck,
  scriptPathsIn,
  svelteKitClientEnvChecks,
  svelteKitPublicValueHandedCheck,
} from "./clientEnv";
import type { CheckContext, Fetch } from "./context";

const BASE = "https://web-j-1-client-env-next.journey.test";
const DIGEST = createHash("sha256").update(CLIENT_ENV_SENSITIVE_VALUE).digest("hex");

function served(paths: Record<string, string>): Fetch {
  return async (input) => {
    const { pathname } = new URL(input instanceof Request ? input.url : String(input));
    const body = paths[pathname];
    return body === undefined ? new Response("missing", { status: 404 }) : new Response(body);
  };
}

function context(fetch: Fetch): CheckContext {
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
    runInEnvironment: async () => "",
    journeyNonce: "journey-nonce",
    projectDir: "/nonexistent",
    tempDir: "/nonexistent",
  };
}

function page({
  publicValue = CLIENT_ENV_PUBLIC_VALUE,
  url = BASE,
  digest = DIGEST,
  head = "",
}: {
  publicValue?: string;
  url?: string;
  digest?: string;
  head?: string;
} = {}): string {
  return `<html><head>${head}</head><body><p data-public-value="${publicValue}" data-deployment-url="${url}" data-sensitive-digest="${digest}">rendered</p></body></html>`;
}

describe("the client-env checks", () => {
  it("set the Next app's keys for a Next fixture and the SvelteKit app's for a SvelteKit one", () => {
    expect(clientEnvValuesOf(nextClientEnvChecks)).toEqual({
      NEXT_PUBLIC_GREETING: CLIENT_ENV_PUBLIC_VALUE,
      SENSITIVE_TOKEN: CLIENT_ENV_SENSITIVE_VALUE,
    });
    expect(clientEnvValuesOf(svelteKitClientEnvChecks)).toEqual({
      PUBLIC_GREETING: CLIENT_ENV_PUBLIC_VALUE,
      SENSITIVE_TOKEN: CLIENT_ENV_SENSITIVE_VALUE,
    });
    expect(clientEnvValuesOf(buildVariablesChecks)).toEqual({});
  });

  describe("the scripts a page loads", () => {
    it("reads root-relative and dot-relative script paths once each, with their query", () => {
      const html = `<script src="/_next/static/chunks/a.js?dpl=x&amp;y=1" async></script>
        <link rel="modulepreload" href="./_app/immutable/entry/start.B1.js">
        <script>self.__next_f.push([1,"\\"/_next/static/chunks/a.js?dpl=x&y=1\\""])</script>
        <script>import("./_app/immutable/entry/start.B1.js")</script>
        <link rel="stylesheet" href="/_next/static/css/a.css">`;
      expect(scriptPathsIn(html)).toEqual([
        "/_next/static/chunks/a.js?dpl=x&y=1",
        "./_app/immutable/entry/start.B1.js",
      ]);
    });

    it("reads only the scripts written inline", () => {
      const html = `<script src="/a.js"></script><script type="module">start({ env: {} })</script>`;
      expect(inlineScriptsIn(html)).toEqual(["start({ env: {} })"]);
    });
  });

  describe("the page rendered on the server", () => {
    it("passes when it renders the public value and the sensitive value's digest", async () => {
      await clientEnvRenderedCheck.run(context(served({ "/": page() })));
    });

    it("fails when the sensitive value it read is not the one set", async () => {
      const fetch = served({ "/": page({ digest: "0" }) });
      await expect(clientEnvRenderedCheck.run(context(fetch))).rejects.toThrow();
    });
  });

  describe("the deployment url", () => {
    it("passes when the page renders an absolute url", async () => {
      await clientEnvDeploymentUrlCheck.run(context(served({ "/": page() })));
    });

    it("fails when the page renders none", async () => {
      const fetch = served({ "/": page({ url: "" }) });
      await expect(clientEnvDeploymentUrlCheck.run(context(fetch))).rejects.toThrow(/no url/);
    });
  });

  describe("the sensitive value", () => {
    const head = `<script src="/chunks/page.js"></script>`;

    it("passes when neither the page nor its scripts hold it", async () => {
      const fetch = served({ "/": page({ head }), "/chunks/page.js": "render()" });
      await clientEnvSensitiveUnsentCheck.run(context(fetch));
    });

    it("fails naming the script that holds it", async () => {
      const fetch = served({
        "/": page({ head }),
        "/chunks/page.js": `const t = "${CLIENT_ENV_SENSITIVE_VALUE}"`,
      });
      await expect(clientEnvSensitiveUnsentCheck.run(context(fetch))).rejects.toThrow(
        /chunks\/page\.js/,
      );
    });

    it("fails naming a chunk a loaded script imports, however deep", async () => {
      const fetch = served({
        "/": page({ head }),
        "/chunks/page.js": `import{a}from"./shared.js";import("../nodes/2.js")`,
        "/chunks/shared.js": `export const a=1`,
        "/nodes/2.js": `import "/chunks/deep.js";`,
        "/chunks/deep.js": `const t = "${CLIENT_ENV_SENSITIVE_VALUE}"`,
      });
      await expect(clientEnvSensitiveUnsentCheck.run(context(fetch))).rejects.toThrow(
        /chunks\/deep\.js/,
      );
    });

    it("reads a script once for each query it is loaded with", async () => {
      const bodies: Record<string, string> = {
        "?one": "render()",
        "?two": `const t = "${CLIENT_ENV_SENSITIVE_VALUE}"`,
      };
      const fetch: Fetch = async (input) => {
        const url = new URL(input instanceof Request ? input.url : String(input));
        if (url.pathname === "/") {
          return new Response(
            page({
              head: `<script src="/chunk.js?one"></script><script src="/chunk.js?two"></script>`,
            }),
          );
        }
        return new Response(bodies[url.search] ?? "missing", {
          status: url.search in bodies ? 200 : 404,
        });
      };
      await expect(clientEnvSensitiveUnsentCheck.run(context(fetch))).rejects.toThrow(
        /chunk\.js\?two/,
      );
    });

    it("never follows an import to another origin or a bare package name", async () => {
      const fetch = served({
        "/": page({ head }),
        "/chunks/page.js": `import "https://cdn.elsewhere.test/x.js";import "svelte/internal.js";`,
      });
      await clientEnvSensitiveUnsentCheck.run(context(fetch));
    });

    it("fails when the page loads no script to look through", async () => {
      await expect(
        clientEnvSensitiveUnsentCheck.run(context(served({ "/": page() }))),
      ).rejects.toThrow(/no script/);
    });
  });

  describe("the public value in a Next app's browser bundle", () => {
    const head = `<script src="/_next/static/chunks/page.js?dpl=d1"></script>`;

    it("passes when a script the page loads holds the inlined value", async () => {
      const fetch = served({
        "/": page({ head }),
        "/_next/static/chunks/page.js": `JSON.parse("{\\"NEXT_PUBLIC_GREETING\\":\\"${CLIENT_ENV_PUBLIC_VALUE}\\"}")`,
      });
      await nextPublicValueInlinedCheck.run(context(fetch));
    });

    it("fails when no script holds it, though the page renders it", async () => {
      const fetch = served({ "/": page({ head }), "/_next/static/chunks/page.js": "render()" });
      await expect(nextPublicValueInlinedCheck.run(context(fetch))).rejects.toThrow(
        /\/_next\/static\/chunks\/page\.js/,
      );
    });
  });

  describe("the edge route", () => {
    it("passes when it answers the public value", async () => {
      const fetch: Fetch = async () => Response.json({ publicValue: CLIENT_ENV_PUBLIC_VALUE });
      await nextEdgePublicValueCheck.run(context(fetch));
    });

    it("fails when it answers another", async () => {
      const fetch: Fetch = async () => Response.json({ publicValue: "journey-other" });
      await expect(nextEdgePublicValueCheck.run(context(fetch))).rejects.toThrow();
    });
  });

  describe("the public value a SvelteKit client starts with", () => {
    it("passes when an inline script holds it", async () => {
      const head = `<script>kit.start(app, { env: {PUBLIC_GREETING:"${CLIENT_ENV_PUBLIC_VALUE}"} })</script>`;
      await svelteKitPublicValueHandedCheck.run(context(served({ "/": page({ head }) })));
    });

    it("fails when only the rendered markup holds it", async () => {
      const head = `<script>kit.start(app, { env: {} })</script>`;
      await expect(
        svelteKitPublicValueHandedCheck.run(context(served({ "/": page({ head }) }))),
      ).rejects.toThrow(/public value/);
    });
  });
});
