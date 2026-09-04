import { describe, expect, it } from "vitest";
import { AWS_BASE, journeyZone, renderConfig, shapeFor, VPS_BASE } from "./config";
import { evidence } from "./evidence";
import { type Compute, type Edge, specByName } from "./spec";
import type { CellContext } from "./targets/types";

function cell(example: string, compute: Compute, edge?: Edge): CellContext {
  const spec = specByName(example);
  return {
    example: spec,
    name: example,
    mode: "full",
    compute,
    ...(edge === undefined ? {} : { edge }),
    suites: spec.suites,
    dir: "/nowhere",
    slug: `j-1-${example}`,
    runId: "1",
    evidence: evidence("/nowhere"),
  };
}

describe("journeyZone", () => {
  it("falls back to the box's own zone when none is named", () => {
    expect(journeyZone({})).toBe("localhost");
    expect(journeyZone({ OCEL_JOURNEY_ZONE: "  " })).toBe("localhost");
  });

  it("takes the zone named", () => {
    expect(journeyZone({ OCEL_JOURNEY_ZONE: "journeys.example" })).toBe("journeys.example");
  });
});

describe("shapeFor", () => {
  it("overlays the aws example with the cell's compute, edge, dns and hostnames", () => {
    expect(
      shapeFor(cell("workspace", "container", "cloudflare"), "aws", {
        OCEL_JOURNEY_ZONE: "j.example",
        OCEL_JOURNEY_DNS: "cloudflare",
      }),
    ).toEqual({
      base: AWS_BASE,
      slug: "j-1-workspace",
      compute: "container",
      edge: "cloudflare",
      dns: "cloudflare",
      hostnames: { next: "next-j-1-workspace.j.example", express: "express-j-1-workspace.j.example" },
    });
  });

  it("leaves compute to the provider when the cell is serverless, and dns alone off a real zone", () => {
    expect(shapeFor(cell("express", "serverless"), "aws", { OCEL_JOURNEY_ZONE: "j.example" })).toEqual({
      base: AWS_BASE,
      slug: "j-1-express",
      hostnames: { web: "web-j-1-express.j.example" },
    });
  });

  it("hangs a vps cell's hostnames under the box's zone", () => {
    expect(shapeFor(cell("express", "container"), "vps", {})).toEqual({
      base: VPS_BASE,
      slug: "j-1-express",
      hostnames: { web: "web-j-1-express.localhost" },
    });
  });

  it("renames a dev cell and nothing else", () => {
    expect(shapeFor(cell("express", "serverless"), "dev", { OCEL_JOURNEY_ZONE: "j.example" })).toEqual({
      base: AWS_BASE,
      slug: "j-1-express",
    });
  });
});

describe("renderConfig", () => {
  it("spreads the example's own config under the cell's slug", () => {
    expect(renderConfig({ base: AWS_BASE, slug: "j-1-express" })).toBe(
      `import { defineConfig } from "ocel/config";
import base from "./ocel.config.ts";

export default defineConfig({
  ...base,
  slug: "j-1-express",
});
`,
    );
  });

  it("imports each edge from where the product ships it", () => {
    expect(renderConfig({ base: AWS_BASE, slug: "s", edge: "api-gateway" })).toContain(
      'import { apiGateway } from "@ocel/provider-aws/edge";',
    );
    expect(renderConfig({ base: AWS_BASE, slug: "s", edge: "cloudfront" })).toContain(
      'import { cloudfront } from "@ocel/provider-aws/edge";',
    );
    expect(renderConfig({ base: AWS_BASE, slug: "s", edge: "cloudflare" })).toContain(
      'import { cloudflare } from "ocel/edge";',
    );
  });

  it("writes every dimension of a full cell", () => {
    expect(
      renderConfig({
        base: AWS_BASE,
        slug: "j-1-express",
        compute: "container",
        edge: "cloudflare",
        dns: "cloudflare",
        hostnames: { web: "web-j-1-express.j.example" },
      }),
    ).toBe(
      `import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import { cloudflareDns } from "ocel/dns";
import base from "./ocel.config.ts";

const hostnames: Record<string, string> = {"web":"web-j-1-express.j.example"};

export default defineConfig({
  ...base,
  slug: "j-1-express",
  edge: cloudflare(),
  dns: cloudflareDns(),
  apps: base.apps?.map((app) => ({
    ...app,
    compute: "container",
    ...(hostnames[app.name] ? { domains: { production: hostnames[app.name] } } : {}),
  })),
});
`,
    );
  });
});
