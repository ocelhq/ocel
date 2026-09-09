import { describe, expect, it } from "bun:test";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  AWS_BASE,
  GCP_BASE,
  JOURNEY_JSON,
  JOURNEY_TS,
  journeyConfigIn,
  journeyZone,
  renderConfig,
  renderJsonConfig,
  shapeFor,
  sweepShapeFor,
  VPS_BASE,
  writeJourneyConfig,
} from "./config";
import { evidence } from "./evidence";
import { type Concern, specByName } from "./spec";
import type { CellContext } from "./targets/types";
import { cloudflare, container, type Variant } from "./variants";

const TS_BASE = "./ocel.config.ts";

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

describe("sweepShapeFor", () => {
  it("destroys a cell through the edge its variant stood it up behind", () => {
    expect(sweepShapeFor(cell("sdk", "workspace", cloudflare), "j-9-sdk-workspace", {})).toEqual({
      base: AWS_BASE,
      slug: "j-9-sdk-workspace",
      edge: "cloudflare",
    });
  });

  it("names no edge for a base cell", () => {
    expect(sweepShapeFor(cell("deploy", "node"), "j-9-deploy-node", {})).toEqual({
      base: AWS_BASE,
      slug: "j-9-deploy-node",
    });
  });

  it("unbinds through the dns the cell was bound under", () => {
    expect(
      sweepShapeFor(cell("deploy", "node"), "j-9-deploy-node", {
        OCEL_JOURNEY_DNS: "cloudflare",
        OCEL_JOURNEY_ZONE: "j.example",
        OCEL_AWS_VARS_KEY: "arn:aws:kms:key/k",
      }),
    ).toEqual({
      base: AWS_BASE,
      slug: "j-9-deploy-node",
      dns: "cloudflare",
    });
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

  it("seals an aws cell's vars under the key the account brought", () => {
    expect(
      shapeFor(cell("deploy", "node"), "aws", { OCEL_AWS_VARS_KEY: " arn:aws:kms:key/k " }),
    ).toEqual({
      base: AWS_BASE,
      slug: "j-1-deploy-node",
      varsKey: "arn:aws:kms:key/k",
    });
  });

  it("hangs a vps cell's hostnames under the box's zone, and takes no key", () => {
    expect(
      shapeFor(cell("deploy", "node"), "vps", { OCEL_AWS_VARS_KEY: "arn:aws:kms:key/k" }),
    ).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
    });
  });

  it("renames a dev cell and nothing else", () => {
    expect(
      shapeFor(cell("deploy", "node"), "dev", {
        OCEL_JOURNEY_ZONE: "j.example",
        OCEL_AWS_VARS_KEY: "arn:aws:kms:key/k",
      }),
    ).toEqual({
      base: AWS_BASE,
      slug: "j-1-deploy-node",
    });
  });
});

const ARCHED_JSON_BASE = `{
  "slug": "go",
  "provider": { "name": "aws" },
  "apps": [{ "name": "web", "path": "./server", "framework": "go", "arch": "arm64" }]
}
`;

describe("a container cell", () => {
  it("carries no framework on any target, because a container runs the image it is given", () => {
    for (const base of [AWS_BASE, GCP_BASE, VPS_BASE]) {
      expect(renderConfig({ base, slug: "j-1-deploy-node", compute: "container" })).toContain(
        "framework: undefined,",
      );
    }
  });

  it("carries no arch either, because the image names the platform it is built for", () => {
    for (const base of [AWS_BASE, GCP_BASE, VPS_BASE]) {
      expect(renderConfig({ base, slug: "j-1-deploy-node", compute: "container" })).toContain(
        "arch: undefined,",
      );
    }
  });

  it("writes neither key as data, whatever the fixture declared", () => {
    const written = JSON.parse(
      renderJsonConfig(ARCHED_JSON_BASE, {
        base: "./ocel.json",
        slug: "j-1-go",
        compute: "container",
      }),
    ) as { apps: Record<string, unknown>[] };
    expect(written.apps[0]).not.toHaveProperty("framework");
    expect(written.apps[0]).not.toHaveProperty("arch");
  });

  it("leaves the framework the fixture declares where the cell runs serverless", () => {
    expect(
      renderConfig({ base: GCP_BASE, slug: "j-1-deploy-node", compute: "serverless" }),
    ).not.toContain("framework:");
  });
});

describe("renderConfig", () => {
  it("spreads the fixture's own config under the cell's slug", () => {
    expect(renderConfig({ base: TS_BASE, slug: "j-1-node" })).toBe(
      `import { defineConfig } from "ocel/config";
import base from "./ocel.config.ts";

export default defineConfig({
  ...base,
  slug: "j-1-node",
});
`,
    );
  });

  it("leaves the provider alone when no key is brought", () => {
    expect(renderConfig({ base: TS_BASE, slug: "j-1-node" })).not.toContain("provider:");
  });

  it("keeps the fixture's own provider options under the key it seals vars with", () => {
    expect(
      renderConfig({ base: TS_BASE, slug: "j-1-node", varsKey: "arn:aws:kms:key/k" }),
    ).toContain(
      `  provider: { name: "aws", options: { ...(base.provider as { options?: object } | undefined)?.options, varsKey: "arn:aws:kms:key/k" } },`,
    );
  });

  it("imports each edge from where the product ships it", () => {
    expect(renderConfig({ base: TS_BASE, slug: "s", edge: "api-gateway" })).toContain(
      'import { apiGateway } from "ocel/providers/aws/edge";',
    );
    expect(renderConfig({ base: TS_BASE, slug: "s", edge: "cloudfront" })).toContain(
      'import { cloudfront } from "ocel/providers/aws/edge";',
    );
    expect(renderConfig({ base: TS_BASE, slug: "s", edge: "cloudflare" })).toContain(
      'import { cloudflare } from "ocel/edge";',
    );
  });

  it("writes every dimension of a full cell", () => {
    expect(
      renderConfig({
        base: TS_BASE,
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
    framework: undefined,
    arch: undefined,
    ...(hostnames[app.name] ? { domains: { production: hostnames[app.name] } } : {}),
  })),
});
`,
    );
  });
});

const COMMENTED_JSON_BASE = `{
  "$schema": "https://ocel.dev/schema/0.0.1-alpha.0/ocel.schema.json",
  "slug": "go",
  "provider": { "name": "aws" },
  "apps": [
    {
      "name": "web",
      "path": "./server",
      // The architecture the binary is built for, x86_64 unless named:
      // "arch": "arm64",
      "framework": "go"
    }
  ]
}
`;

describe("renderJsonConfig", () => {
  it("overlays a base carrying the comments no bundler would read", () => {
    expect(
      JSON.parse(renderJsonConfig(COMMENTED_JSON_BASE, { base: "./ocel.json", slug: "j-1-go" })),
    ).toEqual({
      $schema: "https://ocel.dev/schema/0.0.1-alpha.0/ocel.schema.json",
      slug: "j-1-go",
      provider: { name: "aws" },
      apps: [{ name: "web", path: "./server", framework: "go" }],
    });
  });

  it("writes every dimension of a full cell as data", () => {
    expect(
      JSON.parse(
        renderJsonConfig(COMMENTED_JSON_BASE, {
          base: "./ocel.json",
          slug: "j-1-go",
          compute: "container",
          edge: "api-gateway",
          dns: "cloudflare",
          hostnames: { web: "web-j-1-go.j.example" },
          varsKey: "arn:aws:kms:key/k",
        }),
      ),
    ).toEqual({
      $schema: "https://ocel.dev/schema/0.0.1-alpha.0/ocel.schema.json",
      slug: "j-1-go",
      provider: { name: "aws", options: { varsKey: "arn:aws:kms:key/k" } },
      edge: { kind: "api-gateway" },
      dns: { kind: "cloudflare" },
      apps: [
        {
          name: "web",
          path: "./server",
          compute: "container",
          domains: { production: "web-j-1-go.j.example" },
        },
      ],
    });
  });

  it("keeps the options the fixture's own provider carries", () => {
    expect(
      JSON.parse(
        renderJsonConfig(
          `{"slug":"go","provider":{"name":"aws","options":{"region":"eu-west-1"}}}`,
          { base: "./ocel.json", slug: "j-1-go", varsKey: "arn:aws:kms:key/k" },
        ),
      ).provider,
    ).toEqual({ name: "aws", options: { region: "eu-west-1", varsKey: "arn:aws:kms:key/k" } });
  });
});

describe("writeJourneyConfig", () => {
  it("writes the overlay in the form the fixture's own base is written in", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-config-"));
    await writeFile(path.join(dir, "ocel.json"), COMMENTED_JSON_BASE, "utf8");
    const file = await writeJourneyConfig(dir, { base: AWS_BASE, slug: "j-1-go" });

    expect(file).toBe(path.join(dir, JOURNEY_JSON));
    expect(journeyConfigIn(dir)).toBe(JOURNEY_JSON);
    expect(JSON.parse(await readFile(file, "utf8")).slug).toBe("j-1-go");
  });

  it("writes a program where the fixture's own base is one", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-config-"));
    await writeFile(path.join(dir, "ocel.config.ts"), "export default {};\n", "utf8");
    const file = await writeJourneyConfig(dir, { base: AWS_BASE, slug: "j-1-node" });

    expect(file).toBe(path.join(dir, JOURNEY_TS));
    expect(journeyConfigIn(dir)).toBe(JOURNEY_TS);
  });
});
