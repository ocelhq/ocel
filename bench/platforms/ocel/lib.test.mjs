import { describe, expect, it } from "vitest";

import { APPS, PINNED, PLATFORMS } from "../../matrix.config.mjs";
import {
  DEFAULT_BASE_DOMAIN,
  MAX_SLUG_LEN,
  PROD_ENV_SEGMENT,
  SLUG_PREFIX,
  appURLProblem,
  baseDomainFor,
  baseDomainProblem,
  cellSlug,
  cellSlugFor,
  functionNameVerdict,
  lambdaFunctionNames,
  pinnedProblems,
  productionEnvProblem,
  productionHost,
  renderOcelConfig,
  resolveAppURL,
  slugProblem,
} from "./lib.mjs";

const OCEL_CELLS = PLATFORMS.filter((platform) => platform.driver === "ocel").flatMap((platform) =>
  APPS.map((app) => ({ app: app.name, platform: platform.id })),
);

const LONGEST_RUN_ID = "18446744073709551615";

describe("cellSlug", () => {
  it("gives every cell a project of its own, so no two share (project, app, env=prod)", () => {
    expect(OCEL_CELLS).toHaveLength(12);
    const slugs = OCEL_CELLS.map((cell) => cellSlug({ runId: LONGEST_RUN_ID, ...cell }));
    expect(new Set(slugs).size).toBe(slugs.length);
  });

  it("names the run, the app and the variant", () => {
    expect(cellSlug({ runId: "1234567890", app: "express", platform: "ocel-bundle-bytecode" })).toBe(
      "bench-1234567890-express-bundle-bytecode",
    );
    expect(cellSlug({ runId: "1234567890", app: "hono", platform: "ocel-trace" })).toBe(
      "bench-1234567890-hono-trace",
    );
  });

  it("is stable, so teardown re-derives what deploy used with no state file", () => {
    const cell = { runId: "abc", app: "fastify", platform: "ocel-trace-bytecode" };
    expect(cellSlug(cell)).toBe(cellSlug(cell));
  });

  it("falls back to local and sanitises anything else", () => {
    expect(cellSlug({ app: "hono", platform: "ocel-bundle" })).toBe("bench-local-hono-bundle");
    expect(cellSlug({ runId: "Feature/Branch_9", app: "hono", platform: "ocel-bundle" })).toBe(
      "bench-feature-branch-9-hono-bundle",
    );
  });

  it("refuses a cell it cannot name", () => {
    expect(() => cellSlug({ runId: "x", app: "express", platform: "" })).toThrow(
      /needs both an app and a platform/,
    );
  });

  it("reads the run id off the environment", () => {
    expect(cellSlugFor({ app: "hono", platform: "ocel-bundle" }, { BENCH_RUN_ID: "77" })).toBe(
      "bench-77-hono-bundle",
    );
    expect(cellSlugFor({ app: "hono", platform: "ocel-bundle" }, { GITHUB_RUN_ID: "88" })).toBe(
      "bench-88-hono-bundle",
    );
  });
});

describe("slugProblem", () => {
  it("passes every cell of a realistic run", () => {
    for (const cell of OCEL_CELLS) {
      const slug = cellSlug({ runId: LONGEST_RUN_ID, ...cell });
      expect(slugProblem(slug), slug).toBeNull();
      expect(slug.length).toBeLessThanOrEqual(MAX_SLUG_LEN);
      expect(slug.startsWith(SLUG_PREFIX)).toBe(true);
    }
  });

  it("truncates an absurd run id rather than overrunning the hostname label", () => {
    const slug = cellSlug({ runId: "x".repeat(200), app: "express", platform: "ocel-bundle-bytecode" });
    expect(slug).toHaveLength(MAX_SLUG_LEN);
    expect(slugProblem(slug)).toBeNull();
  });

  it("catches a slug ocel would reject before anything is deployed", () => {
    expect(slugProblem("bench--local")).toMatch(/single\s+hyphens/);
    expect(slugProblem("Bench-Local")).toMatch(/lowercase/);
    expect(slugProblem("bench-")).toMatch(/lowercase/);
    expect(slugProblem(`bench-${"x".repeat(70)}`)).toMatch(/DNS caps at 63/);
  });
});

describe("baseDomain", () => {
  it("defaults to the zone the previews already live on", () => {
    expect(baseDomainFor({})).toBe(DEFAULT_BASE_DOMAIN);
    expect(baseDomainFor({ BENCH_OCEL_BASE_DOMAIN: "Example.COM" })).toBe("example.com");
  });

  it("puts the cell exactly one label below the zone, where Universal SSL reaches", () => {
    expect(productionHost("bench-1-hono-trace", DEFAULT_BASE_DOMAIN)).toBe("bench-1-hono-trace.ocel.site");
    expect(baseDomainProblem(DEFAULT_BASE_DOMAIN)).toBeNull();
  });

  it("refuses a zone that would break TLS or has no zone at all", () => {
    expect(baseDomainProblem("prod.ocel.site")).toMatch(/Universal SSL/);
    expect(baseDomainProblem("*.ocel.site")).toMatch(/not a zone/);
    expect(baseDomainProblem("")).toMatch(/not a zone/);
  });
});

describe("renderOcelConfig", () => {
  it("declares exactly the one app the workdir holds, at this cell's hostname", () => {
    const config = renderOcelConfig({
      slug: "bench-local-fastify-trace",
      app: "fastify",
      framework: "fastify",
      host: "bench-local-fastify-trace.ocel.site",
    });
    expect(config).toContain(`slug: "bench-local-fastify-trace"`);
    expect(config).toContain(`domains: { production: "bench-local-fastify-trace.ocel.site" }`);
    expect(config.match(/name:/g)).toHaveLength(1);
  });

  it("omits the domain when there is none to declare", () => {
    expect(renderOcelConfig({ slug: "bench-local", app: "hono", framework: "hono" })).not.toContain("domains");
  });
});

describe("resolveAppURL", () => {
  const identity = { host: "bench-local-express-bundle.ocel.site" };
  const edge = `https://${identity.host}`;

  it("attributes by hostname, not by position", () => {
    const result = { appUrls: ["https://someone-else.ocel.site", edge] };
    expect(resolveAppURL(result, identity)).toEqual({ url: edge, problem: null });
  });

  it("names the Function URL trap rather than handing back a URL that 403s", () => {
    const result = { appUrls: ["https://abc123.lambda-url.us-east-1.on.aws/"] };
    const { url, problem } = resolveAppURL(result, identity);
    expect(url).toBeNull();
    expect(problem).toMatch(/Lambda Function URL/);
    expect(problem).toMatch(/403/);
    expect(problem).toMatch(/domains\.production/);
  });

  it("refuses an empty announcement", () => {
    expect(resolveAppURL({ appUrls: [] }, identity).url).toBeNull();
    expect(resolveAppURL({}, identity).problem).toMatch(/nothing in appUrls/);
  });

  it("refuses two entries wearing one hostname", () => {
    const { problem } = resolveAppURL({ appUrls: [edge, `${edge}/`] }, identity);
    expect(problem).toMatch(/2 entries in appUrls/);
  });
});

describe("appURLProblem", () => {
  const identity = { host: "bench-local-express-bundle.ocel.site" };

  it("passes this cell's hostname", () => {
    expect(appURLProblem(`https://${identity.host}/`, identity)).toBeNull();
  });

  it("fails a Function URL even when something handed one straight in", () => {
    expect(appURLProblem("https://abc.lambda-url.eu-west-1.on.aws", identity)).toMatch(
      /never given an edge worker/,
    );
  });

  it("fails another cell's hostname", () => {
    expect(appURLProblem("https://bench-local-hono-trace.ocel.site", identity)).toMatch(
      /may be serving another cell/,
    );
  });

  it("fails a non-URL", () => {
    expect(appURLProblem(null, identity)).toMatch(/is not a URL/);
  });
});

describe("function naming", () => {
  it("reads names out of tagged ARNs", () => {
    const response = {
      ResourceTagMappingList: [
        { ResourceARN: "arn:aws:lambda:us-east-1:1234:function:bench-local-express-abc" },
        { ResourceARN: "arn:aws:s3:::some-bucket" },
      ],
    };
    expect(lambdaFunctionNames(response)).toEqual(["bench-local-express-abc"]);
  });

  it("accepts exactly one", () => {
    expect(functionNameVerdict(["one"], { slug: "s", app: "a" })).toEqual({ name: "one", problem: null });
  });

  it("refuses to guess between two, and says what two means", () => {
    const { name, problem } = functionNameVerdict(["one", "two"], { slug: "s", app: "a" });
    expect(name).toBeNull();
    expect(problem).toMatch(/found 2: one, two/);
    expect(problem).toMatch(/never torn down/);
    expect(problem).toMatch(/no cold start can be forced/);
  });

  it("refuses none", () => {
    expect(functionNameVerdict([], { slug: "s", app: "a" }).name).toBeNull();
  });

  it("scopes the tag filter to the production environment", () => {
    expect(PROD_ENV_SEGMENT).toBe("prod");
    expect(functionNameVerdict([], { slug: "s", app: "a" }).problem).toMatch(/ocel:env=prod/);
  });
});

describe("productionEnvProblem", () => {
  it("passes a production deploy", () => {
    expect(productionEnvProblem({ class: "production" })).toBeNull();
  });

  it("refuses to name a function from any other environment's tags", () => {
    expect(productionEnvProblem({ class: "preview", identity: "pr-1" })).toMatch(/not "production"/);
    expect(productionEnvProblem(undefined)).toMatch(/not "production"/);
  });
});

describe("pinnedProblems", () => {
  const pinnedConfig = { Runtime: "nodejs24.x", MemorySize: 1024, Architectures: ["x86_64"] };

  it("passes a function on the pinned machine", () => {
    expect(pinnedProblems(pinnedConfig, PINNED)).toEqual([]);
  });

  it("catches the next-app memory default, which is not what this benchmark pins", () => {
    expect(pinnedProblems({ ...pinnedConfig, MemorySize: 1769 }, PINNED)).toEqual([
      "memory is 1769 MB, not the pinned 1024 MB",
    ]);
  });

  it("catches a different runtime or architecture", () => {
    const problems = pinnedProblems({ Runtime: "nodejs22.x", MemorySize: 1024, Architectures: ["arm64"] }, PINNED);
    expect(problems).toHaveLength(2);
    expect(problems.join(" ")).toMatch(/nodejs22\.x/);
    expect(problems.join(" ")).toMatch(/arm64/);
  });
});
