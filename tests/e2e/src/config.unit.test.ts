import { afterAll, describe, expect, it } from "bun:test";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  awsSweepOverlay,
  DEFAULT_BASE,
  GCP_BASE,
  JOURNEY_JSON,
  JOURNEY_TS,
  journeyConfigIn,
  journeyZone,
  overlayFor,
  renderConfig,
  renderJsonConfig,
  VPS_BASE,
  vpsZoneOf,
  writeJourneyConfig,
} from "./config";
import { evidence } from "./evidence";
import { deploy, sdk } from "./matrix/fixtures";
import type { Fixture, Variant } from "./matrix/types";
import {
  cloudflare,
  cloudflareInFrontOfContainers,
  cloudflareInFrontOfMixedComputes,
  cloudflareOnABox,
  cloudflareOnGoogleCloud,
  cloudflareTunnel,
  container,
  defaults,
  registry,
} from "./matrix/variants";
import type { CellUnderTest } from "./run/cellRun";

const TS_BASE = "./ocel.config.ts";

function cell(fixture: Fixture, variant: Variant = defaults): CellUnderTest {
  return {
    fixture,
    name: fixture.name,
    variant,
    dir: "/nowhere",
    slug: `j-1-${fixture.name.replace("/", "-")}`,
    runId: "1",
    evidence: evidence("/nowhere"),
    journeyNonce: "journey-nonce",
  };
}

describe("journeyZone", () => {
  it("falls back to the box's own zone when none is named", () => {
    expect(journeyZone({})).toBe("localhost");
    expect(journeyZone({ OCEL_E2E_ZONE: "  " })).toBe("localhost");
  });

  it("takes the zone named", () => {
    expect(journeyZone({ OCEL_E2E_ZONE: "journeys.example" })).toBe("journeys.example");
  });
});

describe("vpsZoneOf", () => {
  it("puts a cell Cloudflare fronts under the zone the run names", () => {
    expect(vpsZoneOf(cell(deploy.node, cloudflareOnABox), { OCEL_E2E_ZONE: "j.example" })).toBe(
      "j.example",
    );
  });

  it("keeps a cell nothing fronts under the box's own zone, whatever zone the run names", () => {
    expect(vpsZoneOf(cell(deploy.node), { OCEL_E2E_ZONE: "j.example" })).toBe("localhost");
  });
});

describe("awsSweepOverlay", () => {
  it("destroys a cell through the edge its variant deployed it behind", () => {
    expect(awsSweepOverlay(cell(sdk.workspace, cloudflare), "j-9-sdk-workspace", {})).toEqual({
      base: DEFAULT_BASE,
      slug: "j-9-sdk-workspace",
      edge: "cloudflare",
    });
  });

  it("names no edge for a default cell", () => {
    expect(awsSweepOverlay(cell(deploy.node), "j-9-deploy-node", {})).toEqual({
      base: DEFAULT_BASE,
      slug: "j-9-deploy-node",
    });
  });

  it("unbinds through the dns the cell was bound under", () => {
    expect(
      awsSweepOverlay(cell(deploy.node), "j-9-deploy-node", {
        OCEL_E2E_DNS: "cloudflare",
        OCEL_E2E_ZONE: "j.example",
        OCEL_AWS_VARIABLES_KEY: "arn:aws:kms:key/k",
      }),
    ).toEqual({
      base: DEFAULT_BASE,
      slug: "j-9-deploy-node",
      dns: "cloudflare",
    });
  });
});

describe("overlayFor", () => {
  it("overlays the aws fixture with the variant's config, the dns and the hostnames", () => {
    expect(
      overlayFor(cell(sdk.workspace, cloudflare), "aws", {
        OCEL_E2E_ZONE: "j.example",
        OCEL_E2E_DNS: "cloudflare",
      }),
    ).toEqual({
      base: DEFAULT_BASE,
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
    expect(overlayFor(cell(deploy.node, container), "aws", {})).toEqual({
      base: DEFAULT_BASE,
      slug: "j-1-deploy-node",
      compute: "container",
    });
  });

  it("leaves the fixture's config alone for a default cell, and dns alone off a real zone", () => {
    expect(overlayFor(cell(deploy.node), "aws", { OCEL_E2E_ZONE: "j.example" })).toEqual({
      base: DEFAULT_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.j.example" },
    });
  });

  it("seals an aws cell's variables under the key the account brought", () => {
    expect(
      overlayFor(cell(deploy.node), "aws", { OCEL_AWS_VARIABLES_KEY: " arn:aws:kms:key/k " }),
    ).toEqual({
      base: DEFAULT_BASE,
      slug: "j-1-deploy-node",
      variablesKey: "arn:aws:kms:key/k",
    });
  });

  it("hangs a vps cell's hostnames under the box's zone, and takes no key", () => {
    expect(
      overlayFor(cell(deploy.node), "vps", { OCEL_AWS_VARIABLES_KEY: "arn:aws:kms:key/k" }),
    ).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
    });
  });

  it("keeps a vps cell nothing fronts off the run's zone, since no record would reach it there", () => {
    expect(overlayFor(cell(deploy.node), "vps", { OCEL_E2E_ZONE: "j.example" })).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
    });
  });

  it("pushes a vps registry cell as the user the run names, under the token's variable", () => {
    expect(
      overlayFor(cell(deploy.node, registry), "vps", {
        OCEL_E2E_REGISTRY_USER: "octocat",
        OCEL_E2E_REGISTRY_TOKEN: "ghs_never-written",
      }),
    ).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
      registry: {
        server: "ghcr.io/ocelhq/journey-vps",
        username: "octocat",
        password: "${OCEL_E2E_REGISTRY_TOKEN}",
      },
    });
  });

  it("puts a vps cell behind the proxy the run's front names", () => {
    expect(overlayFor(cell(deploy.node), "vps", { OCEL_VPS_FRONT: "nginx" })).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.localhost" },
      proxy: "manual",
    });
  });

  it("refuses a front the run names and no directory provides", () => {
    expect(() => overlayFor(cell(deploy.node), "vps", { OCEL_VPS_FRONT: "haproxy" })).toThrow(
      /haproxy.*nginx/s,
    );
  });

  it("refuses a vps registry cell when the run names no user to push as", () => {
    expect(() => overlayFor(cell(deploy.node, registry), "vps", {})).toThrow(
      /OCEL_E2E_REGISTRY_USER/,
    );
  });

  it("fronts a vps cloudflare cell with the cloudflare edge under the run's zone and dns", () => {
    expect(
      overlayFor(cell(deploy.node, cloudflareOnABox), "vps", {
        OCEL_E2E_ZONE: "j.example",
        OCEL_E2E_DNS: "cloudflare",
      }),
    ).toEqual({
      base: VPS_BASE,
      slug: "j-1-deploy-node",
      hostnames: { web: "web-j-1-deploy-node.j.example" },
      edge: "cloudflare",
      dns: "cloudflare",
    });
  });

  it("reaches a vps cloudflare-tunnel cell through the cloudflare edge's tunnel", () => {
    const overlay = overlayFor(cell(deploy.node, cloudflareTunnel), "vps", {
      OCEL_E2E_ZONE: "j.example",
    });
    expect(overlay).toMatchObject({ edge: "cloudflare", tunnel: true, dns: "cloudflare" });
    expect(renderConfig({ ...overlay, base: TS_BASE })).toContain(
      "  edge: cloudflare({ tunnel: true }),",
    );
    expect(JSON.parse(renderJsonConfig("{}", overlay)).edge).toEqual({
      cloudflare: { tunnel: true },
    });
  });

  it("writes a vps cloudflare cell's records through Cloudflare, since the edge only forwards a record that exists", () => {
    expect(
      overlayFor(cell(deploy.node, cloudflareOnABox), "vps", { OCEL_E2E_ZONE: "j.example" }),
    ).toMatchObject({ edge: "cloudflare", dns: "cloudflare" });
  });

  it("writes a gcp cloudflare cell's records through Cloudflare, since the edge only forwards a record that exists", () => {
    expect(
      overlayFor(cell(deploy.node, cloudflareOnGoogleCloud), "gcp", {
        OCEL_E2E_ZONE: "j.example",
      }),
    ).toMatchObject({ edge: "cloudflare", dns: "cloudflare" });
  });

  it("fronts a gcp cloudflare cell with the cloudflare edge, and binds its hostnames under the run's zone", () => {
    expect(
      overlayFor(cell(deploy.node, cloudflareOnGoogleCloud), "gcp", {
        OCEL_E2E_ZONE: "j.example",
        OCEL_E2E_DNS: "cloudflare",
      }),
    ).toMatchObject({
      base: GCP_BASE,
      edge: "cloudflare",
      dns: "cloudflare",
      hostnames: { web: expect.stringMatching(/\.j\.example$/) },
    });
  });

  it("writes an aws cell's records through Cloudflare when Cloudflare forwards its containers to their origin", () => {
    expect(
      overlayFor(cell(deploy.node, cloudflareInFrontOfContainers), "aws", {
        OCEL_E2E_ZONE: "j.example",
      }),
    ).toMatchObject({
      base: DEFAULT_BASE,
      edge: "cloudflare",
      compute: "container",
      dns: "cloudflare",
      hostnames: { web: expect.stringMatching(/\.j\.example$/) },
    });
  });

  it("runs one app of an aws cell as a container and writes its records through Cloudflare when Cloudflare fronts a project mixing computes", () => {
    expect(
      overlayFor(cell(deploy.workspace, cloudflareInFrontOfMixedComputes), "aws", {
        OCEL_E2E_ZONE: "j.example",
      }),
    ).toMatchObject({
      edge: "cloudflare",
      computes: { express: "container" },
      dns: "cloudflare",
      hostnames: {
        next: expect.stringMatching(/\.j\.example$/),
        express: expect.stringMatching(/\.j\.example$/),
      },
    });
  });

  it("binds no hostname on a gcp cell no edge fronts", () => {
    expect(overlayFor(cell(deploy.node), "gcp", { OCEL_E2E_ZONE: "j.example" })).not.toHaveProperty(
      "hostnames",
    );
  });

  it("renames a dev cell and nothing else", () => {
    expect(
      overlayFor(cell(deploy.node), "dev", {
        OCEL_E2E_ZONE: "j.example",
        OCEL_AWS_VARIABLES_KEY: "arn:aws:kms:key/k",
      }),
    ).toEqual({
      base: DEFAULT_BASE,
      slug: "j-1-deploy-node",
    });
  });
});

const ARCHED_JSON_BASE = `{
  "slug": "go",
  "provider": "aws",
  "apps": [{ "name": "web", "path": "./server", "framework": "go", "arch": "arm64" }]
}
`;

describe("a container cell", () => {
  it("sets no framework on any target, because a container runs the image it is given", () => {
    for (const base of [DEFAULT_BASE, GCP_BASE, VPS_BASE]) {
      expect(renderConfig({ base, slug: "j-1-deploy-node", compute: "container" })).toContain(
        "framework: undefined,",
      );
    }
  });

  it("sets no arch either, because the image names the platform it is built for", () => {
    for (const base of [DEFAULT_BASE, GCP_BASE, VPS_BASE]) {
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

  it("runs only the apps a mixed cell names as containers, with no framework or arch", () => {
    const rendered = renderConfig({
      base: TS_BASE,
      slug: "j-1-workspace",
      computes: { express: "container" },
    });
    expect(rendered).toContain(
      `...(app.name === "express" ? { compute: "container", framework: undefined, arch: undefined } : {}),`,
    );
    expect(rendered).not.toContain("    compute:");
    const written = JSON.parse(
      renderJsonConfig(ARCHED_JSON_BASE, {
        base: "./ocel.json",
        slug: "j-1-go",
        computes: { web: "container" },
      }),
    ) as { apps: Record<string, unknown>[] };
    expect(written.apps[0]).toMatchObject({ compute: "container" });
    expect(written.apps[0]).not.toHaveProperty("framework");
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

  it("keeps the fixture's own provider options under the key it seals variables with", () => {
    expect(
      renderConfig({ base: TS_BASE, slug: "j-1-node", variablesKey: "arn:aws:kms:key/k" }),
    ).toContain(
      `  provider: { aws: { ...(base.provider !== null && typeof base.provider === "object" ? base.provider.aws : {}), variablesKey: "arn:aws:kms:key/k" } },`,
    );
  });

  it("keeps the fixture's own vps options under the proxy its box runs behind", () => {
    expect(renderConfig({ base: TS_BASE, slug: "j-1-node", proxy: "manual" })).toContain(
      `  provider: { vps: { ...(base.provider !== null && typeof base.provider === "object" ? base.provider.vps : {}), proxy: "manual" } },`,
    );
  });

  it("pushes to the registry the cell names", () => {
    expect(
      renderConfig({
        base: TS_BASE,
        slug: "s",
        registry: { server: "ghcr.io/acme/j", username: "octocat", password: "${TOKEN}" },
      }),
    ).toContain(
      `  registry: {"server":"ghcr.io/acme/j","username":"octocat","password":"\${TOKEN}"},`,
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
  "$schema": "https://ocel.dev/schema/0.0.1/ocel.schema.json",
  "slug": "go",
  "provider": "aws",
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
  it("overlays a base containing the comments no bundler would read", () => {
    expect(
      JSON.parse(renderJsonConfig(COMMENTED_JSON_BASE, { base: "./ocel.json", slug: "j-1-go" })),
    ).toEqual({
      $schema: "https://ocel.dev/schema/0.0.1/ocel.schema.json",
      slug: "j-1-go",
      provider: "aws",
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
          variablesKey: "arn:aws:kms:key/k",
        }),
      ),
    ).toEqual({
      $schema: "https://ocel.dev/schema/0.0.1/ocel.schema.json",
      slug: "j-1-go",
      provider: { aws: { variablesKey: "arn:aws:kms:key/k" } },
      edge: "api-gateway",
      dns: "cloudflare",
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

  it("writes the registry the cell pushes to as data", () => {
    expect(
      JSON.parse(
        renderJsonConfig(COMMENTED_JSON_BASE, {
          base: "./ocel.json",
          slug: "j-1-go",
          registry: { server: "ghcr.io/acme/j", username: "octocat", password: "${TOKEN}" },
        }),
      ).registry,
    ).toEqual({ server: "ghcr.io/acme/j", username: "octocat", password: "${TOKEN}" });
  });

  it("keeps the options the fixture's own provider sets", () => {
    expect(
      JSON.parse(
        renderJsonConfig(`{"slug":"go","provider":{"aws":{"region":"eu-west-1"}}}`, {
          base: "./ocel.json",
          slug: "j-1-go",
          variablesKey: "arn:aws:kms:key/k",
        }),
      ).provider,
    ).toEqual({ aws: { region: "eu-west-1", variablesKey: "arn:aws:kms:key/k" } });
  });

  it("keeps the box the fixture's vps provider reaches under the proxy it runs behind", () => {
    expect(
      JSON.parse(
        renderJsonConfig(`{"slug":"go","provider":{"vps":{"ssh":{"host":"box"}}}}`, {
          base: "./ocel.vps.json",
          slug: "j-1-go",
          proxy: { manual: { port: 9000 } },
        }),
      ).provider,
    ).toEqual({ vps: { ssh: { host: "box" }, proxy: { manual: { port: 9000 } } } });
  });

  it("seals variables under aws when the fixture's provider is null", () => {
    expect(
      JSON.parse(
        renderJsonConfig(`{"slug":"go","provider":null}`, {
          base: "./ocel.json",
          slug: "j-1-go",
          variablesKey: "arn:aws:kms:key/k",
        }),
      ).provider,
    ).toEqual({ aws: { variablesKey: "arn:aws:kms:key/k" } });
  });
});

describe("writeJourneyConfig", () => {
  const dirs: string[] = [];

  afterAll(async () => {
    await Promise.all(dirs.map((dir) => rm(dir, { recursive: true, force: true })));
  });

  it("writes the overlay in the form the fixture's own base is written in", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-config-"));
    dirs.push(dir);
    await writeFile(path.join(dir, "ocel.json"), COMMENTED_JSON_BASE, "utf8");
    const file = await writeJourneyConfig(dir, { base: DEFAULT_BASE, slug: "j-1-go" });

    expect(file).toBe(path.join(dir, JOURNEY_JSON));
    expect(journeyConfigIn(dir)).toBe(JOURNEY_JSON);
    expect(JSON.parse(await readFile(file, "utf8")).slug).toBe("j-1-go");
  });

  it("writes a program where the fixture's own base is one", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "journey-config-"));
    dirs.push(dir);
    await writeFile(path.join(dir, "ocel.config.ts"), "export default {};\n", "utf8");
    const file = await writeJourneyConfig(dir, { base: DEFAULT_BASE, slug: "j-1-node" });

    expect(file).toBe(path.join(dir, JOURNEY_TS));
    expect(journeyConfigIn(dir)).toBe(JOURNEY_TS);
  });
});
