import { join } from "node:path";

import { PINNED } from "../../matrix.config.mjs";

export const SLUG_PREFIX = "bench-";

export const STATE_FILE = ".ocel-bench.json";

export const LOG_FILE = ".bench-ocel.log";

export const DEPLOY_RESULT_FILE = join(".ocel", "deploy-result.json");

export const DESTROY_BYPASS_ENV = "OCEL_DESTROY_BYPASS_CONFIRMATION";

export const DNS_LABEL_MAX_LEN = 63;

export const MAX_SLUG_LEN = DNS_LABEL_MAX_LEN;

export const DEFAULT_BASE_DOMAIN = "ocel.site";

export const PROD_ENV_SEGMENT = "prod";

const LOCAL_RUN_ID = "local";

const SLUG_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

function sanitizeToken(value) {
  return String(value ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

export function variantOf(platform) {
  return sanitizeToken(String(platform ?? "").replace(/^ocel-/, ""));
}

export function cellSlug({ runId, app, platform }) {
  const name = sanitizeToken(app);
  const variant = variantOf(platform);
  if (!name || !variant) {
    throw new Error(
      `a bench project slug needs both an app and a platform; got ${JSON.stringify({ app, platform })}`,
    );
  }
  const tail = `-${name}-${variant}`;
  const budget = MAX_SLUG_LEN - SLUG_PREFIX.length - tail.length;
  if (budget < 1) {
    throw new Error(
      `${SLUG_PREFIX}<run>${tail} cannot fit in ${MAX_SLUG_LEN} characters even with an empty run id; ` +
        `shorten the app or platform name`,
    );
  }
  const run = sanitizeToken(runId) || LOCAL_RUN_ID;
  return SLUG_PREFIX + run.slice(0, budget).replace(/-+$/, "") + tail;
}

export function cellSlugFor({ app, platform }, env = process.env) {
  return cellSlug({ runId: env.BENCH_RUN_ID || env.GITHUB_RUN_ID, app, platform });
}

export function baseDomainFor(env = process.env) {
  return String(env.BENCH_OCEL_BASE_DOMAIN || DEFAULT_BASE_DOMAIN).toLowerCase();
}

export function productionHost(slug, baseDomain) {
  return `${slug}.${baseDomain}`;
}

export function slugProblem(slug) {
  const value = String(slug ?? "");
  if (!SLUG_PATTERN.test(value) || value.includes("--")) {
    return (
      `${JSON.stringify(value)} is not a project slug ocel accepts: lowercase letters, digits and single ` +
      `hyphens, starting and ending with a letter or digit`
    );
  }
  if (value.length > MAX_SLUG_LEN) {
    return (
      `${value} is ${value.length} characters, and it is the leftmost label of this cell's production ` +
      `hostname, which DNS caps at ${DNS_LABEL_MAX_LEN} — shorten the run id the slug is built from`
    );
  }
  return null;
}

export function baseDomainProblem(baseDomain) {
  const value = String(baseDomain ?? "").toLowerCase();
  if (!value || value.startsWith("*") || value.split(".").length < 2) {
    return (
      `${JSON.stringify(baseDomain)} is not a zone this cell can be served on; set BENCH_OCEL_BASE_DOMAIN to a ` +
      `bare zone in the Cloudflare account the deploy uses, e.g. ${DEFAULT_BASE_DOMAIN}`
    );
  }
  if (value.split(".").length > 2) {
    return (
      `${value} is more than one label deep, so ${productionHost("<cell>", value)} sits below what the zone's ` +
      `Universal SSL certificate covers and TLS would fail there — use the zone apex, e.g. ${DEFAULT_BASE_DOMAIN}`
    );
  }
  return null;
}

export function renderOcelConfig({ slug, app, framework, host }) {
  const domains = host ? `, domains: { production: ${JSON.stringify(host)} }` : "";
  return [
    `import { defineConfig } from "ocel/config";`,
    `import awsProvider from "@ocel/provider-aws";`,
    ``,
    `export default defineConfig({`,
    `  slug: ${JSON.stringify(slug)},`,
    `  provider: awsProvider(),`,
    `  apps: [{ name: ${JSON.stringify(app)}, path: ".", framework: ${JSON.stringify(framework)}${domains} }],`,
    `});`,
    ``,
  ].join("\n");
}

export const FUNCTION_URL_HOST = /\.lambda-url\.[a-z0-9-]+\.on\.aws$/;

export function isFunctionURLHost(host) {
  return FUNCTION_URL_HOST.test(String(host ?? "").toLowerCase());
}

export function urlHost(url) {
  try {
    return new URL(String(url)).host.toLowerCase();
  } catch {
    return "";
  }
}

export function resolveAppURL(result, { host }) {
  const urls = Array.isArray(result?.appUrls) ? result.appUrls.map(String) : [];
  const matched = urls.filter((url) => urlHost(url) === host);
  if (matched.length > 1) {
    return {
      url: null,
      problem:
        `${matched.length} entries in appUrls carry the hostname ${host} (${matched.join(", ")}); ` +
        `one app in one project has exactly one production hostname`,
    };
  }
  if (matched.length === 1) {
    return { url: matched[0], problem: null };
  }
  const functionURLs = urls.filter((url) => isFunctionURLHost(urlHost(url)));
  if (functionURLs.length > 0) {
    return {
      url: null,
      problem:
        `nothing in appUrls carries this cell's hostname ${host}; the deploy announced ` +
        `${functionURLs.join(", ")} instead, which is the Lambda Function URL it falls back to when an app is ` +
        `given no hostname to serve. That URL is created with AuthorizationType: IAM and 403s every unsigned ` +
        `request, so measuring latency against it would measure a rejection — check that ocel.config.ts declares ` +
        `domains.production and that the zone is in the Cloudflare account this deploy used`,
    };
  }
  return {
    url: null,
    problem:
      `nothing in appUrls carries this cell's hostname ${host}; appUrls is ${JSON.stringify(urls)}. ` +
      `An app that got its edge worker is announced at the hostname it was given`,
  };
}

export function appURLProblem(url, { host }) {
  const found = urlHost(url);
  if (!found) return `${JSON.stringify(url)} is not a URL`;
  if (isFunctionURLHost(found)) {
    return (
      `${found} is a Lambda Function URL, not this cell's hostname ${host} — the app was never given an edge ` +
      `worker, and an IAM-authed Function URL 403s every unsigned request`
    );
  }
  if (found !== host) {
    return `${found} is not this cell's hostname ${host}, so it may be serving another cell's deployment`;
  }
  return null;
}

export function productionEnvProblem(environment) {
  const found = environment?.class;
  if (found === "production") return null;
  return (
    `${DEPLOY_RESULT_FILE} reports the environment class as ${JSON.stringify(found)}, not "production". ` +
    `This driver names the deployed function by its ocel:env=${PROD_ENV_SEGMENT} tag, and any other class ` +
    `carries a different tag, so the lookup would either find nothing or find another environment's function`
  );
}

export function lambdaFunctionNames(response) {
  return (response?.ResourceTagMappingList ?? [])
    .map((entry) => /^arn:aws:lambda:[^:]*:[^:]*:function:([^:]+)/.exec(entry?.ResourceARN ?? ""))
    .filter(Boolean)
    .map((match) => match[1]);
}

export function functionNameVerdict(names, { slug, app }) {
  const found = names ?? [];
  if (found.length === 1) return { name: found[0], problem: null };
  return {
    name: null,
    problem:
      `expected exactly one lambda function tagged ocel:project=${slug} ocel:app=${app} ` +
      `ocel:env=${PROD_ENV_SEGMENT}, found ${found.length}${found.length ? `: ${found.join(", ")}` : ""}. ` +
      `Every cell deploys into a project slug of its own, so more than one means an earlier deploy of this same ` +
      `cell was never torn down. Without one name no cold start can be forced and no REPORT line can be attributed`,
  };
}

export function pinnedProblems(config, pinned = PINNED) {
  const problems = [];
  const runtime = config?.Runtime;
  if (runtime !== pinned.runtime) {
    problems.push(`runtime is ${JSON.stringify(runtime)}, not the pinned ${pinned.runtime}`);
  }
  const memory = config?.MemorySize;
  if (memory !== pinned.memoryMB) {
    problems.push(`memory is ${JSON.stringify(memory)} MB, not the pinned ${pinned.memoryMB} MB`);
  }
  const architectures = config?.Architectures ?? [];
  if (architectures.length !== 1 || architectures[0] !== pinned.architecture) {
    problems.push(`architecture is ${JSON.stringify(architectures)}, not the pinned [${pinned.architecture}]`);
  }
  return problems;
}

const NATIVE_SUFFIX = { linux: "linux-x64", darwin: `darwin-${process.arch}`, win32: "win32-x64" };

export function nativeProviderPackage(platform = process.platform) {
  const suffix = NATIVE_SUFFIX[platform];
  if (!suffix) {
    throw new Error(`no ocel native provider package for ${platform}/${process.arch}`);
  }
  return `provider-aws-${suffix}`;
}

export function tail(text, maxLines) {
  const lines = String(text ?? "").split("\n");
  return lines.slice(Math.max(0, lines.length - maxLines)).join("\n");
}
