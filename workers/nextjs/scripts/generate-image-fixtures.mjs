import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { animatedGif, ico, notAnImage, png, svg } from "./fixture-assets.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const workerRoot = join(here, "..");
const repoRoot = join(workerRoot, "..", "..");
const appRoot = join(repoRoot, "examples", "next-test");
const fixtureFile = join(workerRoot, "test", "fixtures", "image-conformance.json");
const port = Number(process.env.OCEL_IMAGE_FIXTURE_PORT ?? 3111);
const origin = `http://127.0.0.1:${port}`;

const BROWSER_ACCEPT =
  "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8";

const ASSETS = {
  "public/_fixtures/photo.png": png(),
  "public/_fixtures/animated.gif": animatedGif(),
  "public/_fixtures/icon.ico": ico(),
  "public/_fixtures/vector.svg": svg(),
  "public/_fixtures/not-an-image.txt": notAnImage(),
  "app/image-fixtures/photo.png": png(),
};

const PHOTO = "/_fixtures/photo.png";

const CASES = [
  { name: "url-missing", query: { w: "640", q: "75" } },
  { name: "url-empty", query: { url: "", w: "640", q: "75" } },
  { name: "url-array", query: { url: [PHOTO, "/_fixtures/icon.ico"], w: "640", q: "75" } },
  {
    name: "url-too-long",
    query: { url: `/${"a".repeat(3072)}`, w: "640", q: "75" },
    note: "3073 characters, one past the cap",
  },
  { name: "url-protocol-relative", query: { url: "//evil.example/img/a.png", w: "640", q: "75" } },
  {
    name: "url-protocol-relative-allowlisted-host",
    query: { url: "//legacy.example/a.png", w: "640", q: "75" },
    note: "the // check must precede the relative/absolute branch or this is a full allowlist bypass",
  },
  { name: "url-recursive", query: { url: "/_next/image?url=%2Ffoo.png", w: "640", q: "75" } },
  {
    name: "url-recursive-percent-encoded",
    query: { url: "/_next/%69mage", w: "640", q: "75" },
    note: "the guard decodes before matching, so a prefix check would miss this",
  },
  {
    name: "url-recursive-behind-asset-prefix",
    query: { url: "/cdn-prefix/_next/image", w: "640", q: "75" },
    note: "matched anywhere in the pathname, not as a prefix",
  },
  {
    name: "url-local-with-search",
    query: { url: `${PHOTO}?v=1`, w: "640", q: "75" },
    note: "localPatterns defaults to search: \"\", so any query string is refused",
  },
  { name: "url-local-allowed", query: { url: PHOTO, w: "640", q: "75" }, accept: BROWSER_ACCEPT },
  { name: "url-absolute-unparseable", query: { url: "https://[", w: "640", q: "75" } },
  { name: "url-absolute-ftp", query: { url: "ftp://cdn.allowed.example/img/a.png", w: "640", q: "75" } },
  { name: "url-absolute-data", query: { url: "data:image/png;base64,AAAA", w: "640", q: "75" } },
  { name: "url-absolute-denied", query: { url: "https://evil.example/img/a.png", w: "640", q: "75" } },
  {
    name: "url-absolute-suffix-attack",
    query: { url: "https://allowed.example.evil.example/img/a.png", w: "640", q: "75" },
    note: "the compiled hostname regex is anchored, so a suffixed host is not a match",
  },
  {
    name: "url-absolute-userinfo-attack",
    query: { url: "https://cdn.allowed.example@evil.example/img/a.png", w: "640", q: "75" },
    note: "userinfo is not hostname",
  },
  {
    name: "url-absolute-wrong-pathname",
    query: { url: "https://cdn.allowed.example/other/a.png", w: "640", q: "75" },
  },
  {
    name: "url-absolute-allowed",
    query: { url: "https://cdn.allowed.example/img/a.png", w: "640", q: "75" },
    note: "passes validation; the fetch then fails, which is the origin's problem, not the edge's",
  },
  {
    name: "url-absolute-allowed-with-port",
    query: { url: "https://cdn.allowed.example:8443/img/a.png", w: "640", q: "75" },
    note: "matching is against url.hostname — url.host would carry :8443 and fail the pattern",
  },
  {
    name: "url-absolute-allowed-domain",
    query: { url: "https://legacy.example/anything.png", w: "640", q: "75" },
  },
  { name: "w-missing", query: { url: PHOTO, q: "75" } },
  { name: "w-array", query: { url: PHOTO, w: ["640", "750"], q: "75" } },
  {
    name: "w-fractional",
    query: { url: PHOTO, w: "99.9", q: "75" },
    note: "the integer regex must run before parseInt, which would read this as 99",
  },
  { name: "w-not-a-number", query: { url: PHOTO, w: "abc", q: "75" } },
  { name: "w-negative", query: { url: PHOTO, w: "-640", q: "75" } },
  { name: "w-zero", query: { url: PHOTO, w: "0", q: "75" } },
  { name: "w-not-in-sizes", query: { url: PHOTO, w: "641", q: "75" } },
  {
    name: "w-fractional-and-q-missing",
    query: { url: PHOTO, w: "99.9" },
    note: "pins the interleaving: the w regex is checked before q's presence",
  },
  {
    name: "w-zero-and-q-missing",
    query: { url: PHOTO, w: "0" },
    note: "pins the interleaving: q's presence is checked before w's value",
  },
  { name: "q-missing", query: { url: PHOTO, w: "640" } },
  { name: "q-array", query: { url: PHOTO, w: "640", q: ["75", "50"] } },
  { name: "q-fractional", query: { url: PHOTO, w: "640", q: "75.5" } },
  { name: "q-zero", query: { url: PHOTO, w: "640", q: "0" } },
  { name: "q-over-100", query: { url: PHOTO, w: "640", q: "101" } },
  { name: "q-not-in-qualities", query: { url: PHOTO, w: "640", q: "50" } },
  { name: "w-and-q-in-image-sizes", query: { url: PHOTO, w: "32", q: "75" }, accept: BROWSER_ACCEPT },

  {
    name: "accept-absent",
    query: { url: PHOTO, w: "640", q: "75" },
    note: "no Accept at all negotiates nothing; the output keeps the source format",
  },
  {
    name: "accept-any",
    query: { url: PHOTO, w: "640", q: "75" },
    accept: "*/*",
    note: "getSupportedMimeType's literal accept.includes() guard makes this negotiate nothing",
  },
  {
    name: "accept-image-wildcard",
    query: { url: PHOTO, w: "640", q: "75" },
    accept: "image/*",
    note: "same guard: a wildcard subtype is resolved and then discarded",
  },
  { name: "accept-browser", query: { url: PHOTO, w: "640", q: "75" }, accept: BROWSER_ACCEPT },
  { name: "accept-webp-only", query: { url: PHOTO, w: "640", q: "75" }, accept: "image/webp" },
  {
    name: "accept-avif-only",
    query: { url: PHOTO, w: "640", q: "75" },
    accept: "image/avif",
    note: "avif is not in the configured formats, so nothing is negotiated",
  },
  {
    name: "accept-webp-q-zero",
    query: { url: PHOTO, w: "640", q: "75" },
    accept: "image/webp;q=0,image/png",
  },
  {
    name: "accept-malformed-parameter",
    query: { url: PHOTO, w: "640", q: "75" },
    accept: "image/webp;q",
    note: "registered divergence 6: @hapi/accept throws on a valueless parameter, so Next 500s before it looks at the image; the edge declines to negotiate and lets the request reach the origin",
  },

  {
    name: "animated-gif",
    query: { url: "/_fixtures/animated.gif", w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
  },
  {
    name: "svg",
    query: { url: "/_fixtures/vector.svg", w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
  },
  {
    name: "ico",
    query: { url: "/_fixtures/icon.ico", w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
  },
  {
    name: "not-an-image",
    query: { url: "/_fixtures/not-an-image.txt", w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
  },
  {
    name: "missing-source",
    query: { url: "/_fixtures/absent.png", w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
  },
];

const STATIC_MEDIA_CASES = (mediaPath) => [
  {
    name: "static-import",
    query: { url: mediaPath, w: "640", q: "75" },
    accept: BROWSER_ACCEPT,
    note: "a /_next/static/media source is the only one that gets an immutable Cache-Control",
  },
  {
    name: "static-import-accept-any",
    query: { url: mediaPath, w: "640", q: "75" },
    accept: "*/*",
  },
];

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: "inherit", ...options });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command} ${args.join(" ")} exited ${code}`)),
    );
  });
}

async function startServer(env) {
  const child = spawn(
    join(appRoot, "node_modules", ".bin", "next"),
    ["start", "--port", String(port)],
    { cwd: appRoot, env: { ...process.env, ...env }, stdio: "inherit" },
  );
  const stop = () =>
    new Promise((resolve) => {
      child.once("exit", resolve);
      child.kill("SIGTERM");
    });

  for (let attempt = 0; attempt < 120; attempt++) {
    try {
      await fetch(`${origin}/image-fixtures`);
      return stop;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
  await stop();
  throw new Error(`next start did not come up on ${origin}`);
}

function queryString(query) {
  const params = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    for (const one of Array.isArray(value) ? value : [value]) params.append(name, one);
  }
  return params.toString();
}

async function record(testCase) {
  const search = queryString(testCase.query);
  const headers = testCase.accept === undefined ? {} : { accept: testCase.accept };
  const response = await fetch(`${origin}/_next/image?${search}`, {
    headers,
    redirect: "manual",
  });
  const bytes = Buffer.from(await response.arrayBuffer());
  const contentType = response.headers.get("content-type");
  const textual = response.status !== 200 || /^text\//.test(contentType ?? "");

  return {
    name: testCase.name,
    ...(testCase.note ? { note: testCase.note } : {}),
    request: { search, accept: testCase.accept ?? null },
    status: response.status,
    body: textual
      ? { text: bytes.toString("utf8") }
      : { bytes: bytes.length, sha256: createHash("sha256").update(bytes).digest("hex") },
    contentType,
    cacheControl: response.headers.get("cache-control"),
    vary: response.headers.get("vary"),
    contentDisposition: response.headers.get("content-disposition"),
    csp: response.headers.get("content-security-policy"),
  };
}

async function staticMediaPath() {
  const dir = join(appRoot, ".next", "static", "media");
  const entries = await readdir(dir);
  const emitted = entries.filter((name) => name.endsWith(".png")).sort();
  if (emitted.length === 0) {
    throw new Error(`no static-import image under ${dir}; app/image-fixtures/page.tsx must import one`);
  }
  return `/_next/static/media/${emitted[0]}`;
}

async function compiledConfig() {
  const required = JSON.parse(
    await readFile(join(appRoot, ".next", "required-server-files.json"), "utf8"),
  );
  const { compileImageConfig, imageConfigHash } = await import(
    pathToFileURL(join(repoRoot, "packages", "next-runtime", "dist", "image-config.mjs")).href
  );
  const compiled = compileImageConfig(required.config.images);
  if (!compiled) throw new Error("the fixture app compiled to no image config");
  return { ...compiled, configHash: imageConfigHash(compiled) };
}

async function clientEnv() {
  await run("go", ["run", "github.com/ocelhq/ocel/cli/ocel", "generate"], { cwd: appRoot });
  const accessor = await readFile(join(appRoot, ".ocel", "env-client.ts"), "utf8");
  const env = {};
  for (const [, key] of accessor.matchAll(/process\.env\.([A-Z0-9_]+)/g)) {
    env[key] = "image-fixtures";
  }
  return env;
}

async function generateVariant(variant, baseEnv) {
  const env = { ...baseEnv, OCEL_IMAGE_FIXTURES: variant };
  await run(join(appRoot, "node_modules", ".bin", "next"), ["build"], { cwd: appRoot, env: { ...process.env, ...env } });
  await rm(join(appRoot, ".next", "cache", "images"), { recursive: true, force: true });

  const config = await compiledConfig();
  const cases = [...CASES, ...STATIC_MEDIA_CASES(await staticMediaPath())];

  const stop = await startServer(env);
  try {
    const results = [];
    for (const testCase of cases) results.push(await record(testCase));
    return { variant, config, cases: results };
  } finally {
    await stop();
  }
}

async function main() {
  const require = createRequire(join(appRoot, "package.json"));
  const nextVersion = require("next/package.json").version;

  for (const [path, bytes] of Object.entries(ASSETS)) {
    const file = join(appRoot, path);
    await mkdir(dirname(file), { recursive: true });
    await writeFile(file, bytes);
  }

  await run("pnpm", ["--filter", "@ocel/next-runtime", "build"], { cwd: repoRoot });

  const baseEnv = await clientEnv();
  const variants = [];
  for (const variant of ["default", "svg"]) {
    variants.push(await generateVariant(variant, baseEnv));
  }

  await mkdir(dirname(fixtureFile), { recursive: true });
  await writeFile(
    fixtureFile,
    `${JSON.stringify({ next: nextVersion, variants }, null, 2)}\n`,
  );
  console.log(`wrote ${fixtureFile}`);

  await rm(join(appRoot, ".next"), { recursive: true, force: true });
}

await main();
