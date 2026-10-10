import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { type Check, type CheckContext, describeResponse, json } from "./context";

export const CLIENT_ENV_PUBLIC_VALUE = "journey-public-greeting-every-browser-reads";
export const CLIENT_ENV_SENSITIVE_VALUE = "journey-sensitive-token-never-in-a-browser";

const SENSITIVE_KEY = "SENSITIVE_TOKEN";

const NEXT_VALUES: Record<string, string> = {
  NEXT_PUBLIC_GREETING: CLIENT_ENV_PUBLIC_VALUE,
  [SENSITIVE_KEY]: CLIENT_ENV_SENSITIVE_VALUE,
};

const SVELTEKIT_VALUES: Record<string, string> = {
  PUBLIC_GREETING: CLIENT_ENV_PUBLIC_VALUE,
  [SENSITIVE_KEY]: CLIENT_ENV_SENSITIVE_VALUE,
};

const SCRIPT_PATH = /["'](\.?\/[^"'\s<>\\]+?\.m?js(?:\?[^"'\s<>\\]*)?)\\?["']/g;
const INLINE_SCRIPT = /<script\b(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g;

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function attribute(html: string, name: string): string | undefined {
  return new RegExp(`${name}="([^"]*)"`).exec(html)?.[1];
}

export function scriptPathsIn(html: string): string[] {
  const paths = [...html.matchAll(SCRIPT_PATH)].map((found) =>
    (found[1] ?? "").replaceAll("&amp;", "&"),
  );
  return [...new Set(paths)];
}

export function inlineScriptsIn(html: string): string[] {
  return [...html.matchAll(INLINE_SCRIPT)].map((found) => found[1] ?? "");
}

async function home(ctx: CheckContext): Promise<string> {
  const res = await ctx.fetch(`${ctx.baseUrl}/`, { headers: { accept: "text/html" } });
  const html = await res.text();
  assert.equal(res.status, 200, describeResponse(res, html));
  return html;
}

async function scriptsLoadedBy(ctx: CheckContext, html: string): Promise<Map<string, string>> {
  const paths = scriptPathsIn(html);
  assert.ok(paths.length > 0, "GET / loads no script to look through");
  const loaded = new Map<string, string>();
  for (const one of paths) {
    const url = new URL(one, `${ctx.baseUrl}/`);
    const res = await ctx.fetch(url.href);
    const body = await res.text();
    assert.equal(res.status, 200, `GET ${url.pathname} ${describeResponse(res, body)}`);
    loaded.set(url.pathname, body);
  }
  return loaded;
}

export const clientEnvRenderedCheck: Check = {
  title: "GET / renders the public value and the sensitive value's digest on the server",
  run: async (ctx) => {
    const html = await home(ctx);
    assert.equal(attribute(html, "data-public-value"), CLIENT_ENV_PUBLIC_VALUE);
    assert.equal(attribute(html, "data-sensitive-digest"), sha256(CLIENT_ENV_SENSITIVE_VALUE));
  },
};

export const clientEnvDeploymentUrlCheck: Check = {
  title: "GET / renders the url the deployment is served from on the server",
  run: async (ctx) => {
    const rendered = attribute(await home(ctx), "data-deployment-url") ?? "";
    assert.ok(URL.canParse(rendered), `the page renders ${JSON.stringify(rendered)}, no url`);
    assert.match(new URL(rendered).protocol, /^https?:$/);
  },
};

export const clientEnvSensitiveUnsentCheck: Check = {
  title: "neither GET / nor a script it loads holds the sensitive value",
  assertsDeployment: true,
  run: async (ctx) => {
    const html = await home(ctx);
    assert.ok(!html.includes(CLIENT_ENV_SENSITIVE_VALUE), "GET / holds the sensitive value");
    const holding = [...(await scriptsLoadedBy(ctx, html))]
      .filter(([, body]) => body.includes(CLIENT_ENV_SENSITIVE_VALUE))
      .map(([path]) => path);
    assert.deepEqual(holding, [], "these scripts GET / loads hold the sensitive value");
  },
};

export const nextPublicValueInlinedCheck: Check = {
  title: "a script GET / loads holds the public value Next inlined for the browser",
  assertsDeployment: true,
  run: async (ctx) => {
    const scripts = await scriptsLoadedBy(ctx, await home(ctx));
    assert.ok(
      [...scripts.values()].some((body) => body.includes(CLIENT_ENV_PUBLIC_VALUE)),
      `none of the scripts GET / loads holds the public value: ${[...scripts.keys()].join(", ")}`,
    );
  },
};

export const nextEdgePublicValueCheck: Check = {
  title: "GET /edge reads the public value on the edge runtime",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/edge");
    assert.equal(res.status, 200);
    assert.deepEqual(body, { publicValue: CLIENT_ENV_PUBLIC_VALUE });
  },
};

export const svelteKitPublicValueHandedCheck: Check = {
  title: "GET / starts its client with the public value in an inline script",
  run: async (ctx) => {
    const scripts = inlineScriptsIn(await home(ctx));
    assert.ok(scripts.length > 0, "GET / holds no inline script that starts its client");
    assert.ok(
      scripts.some((body) => body.includes(CLIENT_ENV_PUBLIC_VALUE)),
      "no inline script GET / holds starts its client with the public value",
    );
  },
};

const RENDERED_CHECKS = [
  clientEnvRenderedCheck,
  clientEnvDeploymentUrlCheck,
  clientEnvSensitiveUnsentCheck,
];

export const nextClientEnvChecks: Check[] = [
  ...RENDERED_CHECKS,
  nextPublicValueInlinedCheck,
  nextEdgePublicValueCheck,
];

export const svelteKitClientEnvChecks: Check[] = [
  ...RENDERED_CHECKS,
  svelteKitPublicValueHandedCheck,
];

const VALUES_SET_FOR: ReadonlyMap<Check, Record<string, string>> = new Map([
  [nextPublicValueInlinedCheck, NEXT_VALUES],
  [svelteKitPublicValueHandedCheck, SVELTEKIT_VALUES],
]);

export function clientEnvValuesOf(checks: Check[]): Record<string, string> {
  return Object.assign({}, ...checks.map((one) => VALUES_SET_FOR.get(one) ?? {}));
}
