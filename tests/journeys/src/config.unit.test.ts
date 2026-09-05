import { describe, expect, it } from "bun:test";
import { AWS_BASE, journeyZone, renderConfig, shapeFor, VPS_BASE } from "./config";
import { evidence } from "./evidence";
import { type Concern, specByName } from "./spec";
import type { CellContext } from "./targets/types";
import { cloudflare, container, type Variant } from "./variants";

function cell(concern: Concern, name: string, variant?: Variant): CellContext {
  return {
    fixture: specByName(concern, name),
    name: `${concern}/${name}`,
    ...(variant === undefined ? {} : { variant }),
    dir: "/nowhere",
    slug: `j-1-${concern}-${name}`,
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
  it("overlays the aws fixture with the variant's config, the dns and the hostnames", () => {
    expect(
      shapeFor(cell("sdk", "workspace", cloudflare), "aws", {
        OCEL_JOURNEY_ZONE: "j.example",
        OCEL_JOURNEY_DNS: "cloudflare",
      }),
    ).toEqual({
      base: AWS_BASE,
      slug: "j-1-sdk-workspace",
      edge: "cloudflare",
      dns: "cloudflare",
      hostnames: {
        next: "next-j-1-sdk-workspace.j.example",
        express: "express-j-1-sdk-workspace.j.example",
      },
    });
  });

  it("takes the compute a container variant names", () => {
    expect(shapeFor(cell("deploy", "node", container), "aws", {})).toEqual({
      base: AWS_BASE,
      slug: "j-1-deploy-node",
      compute: "container",
    });
  });

  it("leaves the fixture's config alone for a base cell, and dns alone off a real zone", () => {
    expect(shapeFor(cell("deploy", "node"), "aws", { OCEL_JOURNEY_ZONE: "j.example" })).toEqual({
      base: AWS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.j.example" },
    });
  });

  it("hangs a vps cell's hostnames under the box's zone", () => {
    expect(shapeFor(cell("deploy", "node"), "vps", {})).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
    });
  });

  it("renames a dev cell and nothing else", () => {
    expect(shapeFor(cell("deploy", "node"), "dev", { OCEL_JOURNEY_ZONE: "j.example" })).toEqual({
      base: AWS_BASE,
      slug: "j-1-deploy-node",
    });
  });
});

describe("renderConfig", () => {
  it("spreads the fixture's own config under the cell's slug", () => {
    expect(renderConfig({ base: AWS_BASE, slug: "j-1-node" })).toBe(
      `import { defineConfig } from "ocel/config";
import base from "./ocel.config.ts";

export default defineConfig({
  ...base,
  slug: "j-1-node",
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
        slug: "j-1-node",
        compute: "container",
        edge: "cloudflare",
        dns: "cloudflare",
        hostnames: { web: "web-j-1-node.j.example" },
      }),
    ).toBe(
      `import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import { cloudflareDns } from "ocel/dns";
import base from "./ocel.config.ts";

const hostnames: Record<string, string> = {"web":"web-j-1-node.j.example"};

export default defineConfig({
  ...base,
  slug: "j-1-node",
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
