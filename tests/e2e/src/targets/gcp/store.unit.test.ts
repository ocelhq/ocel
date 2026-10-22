import { afterEach, describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  APP_LABEL,
  ENVIRONMENT_LABEL,
  labelValue,
  NAMESPACE_LABEL,
  PROJECT_LABEL,
  TIER_LABEL,
} from "./names";
import {
  accessToken,
  BOOTSTRAP_APIS,
  exposedServices,
  findAppService,
  reachable,
  readServices,
  revisionTagUrlsOf,
  servicesIn,
  servicesOf,
  strayServices,
  switchedOnAPIs,
} from "./store";

const providerSource = (...at: string[]) =>
  readFile(path.join(repoRoot, "platform", "gcp", "provider", ...at), "utf8");

const quoted = (listed: string | undefined) =>
  [...(listed ?? "").matchAll(/"([^"]+)"/g)].map((one) => one[1]);

const named = (go: string, listed: string | undefined) =>
  (listed ?? "")
    .split(",")
    .map((one) => one.trim())
    .filter(Boolean)
    .map(
      (one) =>
        quoted(one)[0] ?? quoted(go.match(new RegExp(`\\b${one}\\s*=\\s*("[^"]+")`))?.[1])[0],
    );

describe("the apis the floci lane switches on", () => {
  it("are the ones the provider's bootstrap refuses without", async () => {
    const go = await providerSource("internal", "bootstrap", "resources.go");
    const listed = go.match(/var BootstrapAPIs = \[\]string\{([^}]*)\}/);
    expect(listed).not.toBeNull();
    expect(quoted(listed?.[1])).toEqual(BOOTSTRAP_APIS);
  });

  it("are the ones each part the lane adds and each deploy refuses without", async () => {
    const parts = await providerSource("internal", "bootstrap", "resourcepart.go");
    const sync = await providerSource("envsource", "part.go");
    const deploy = await providerSource("preflight.go");
    const partAPIs = [...parts.matchAll(/APIs:\s*\[\]string\{([^}]*)\}/g)].flatMap((one) =>
      quoted(one[1]),
    );
    const syncAPIs = named(sync, sync.match(/var SyncAPIs = \[\]string\{([^}]*)\}/)?.[1]);
    const deployAPIs = quoted(deploy.match(/const \(([^)]*)\)/)?.[1]);
    expect(syncAPIs.length).toBeGreaterThan(0);
    expect(deployAPIs.length).toBeGreaterThan(0);

    const declared = new Set([...BOOTSTRAP_APIS, ...partAPIs, ...syncAPIs, ...deployAPIs]);
    expect(new Set(switchedOnAPIs())).toEqual(declared);
  });
});

describe("accessToken", () => {
  it("asks for a fresh token every time it reaches a real project, since a token outlives no long run", async () => {
    let minted = 0;
    const mint = async () => `token-${++minted}`;

    const first = await accessToken(undefined, mint);
    const second = await accessToken(undefined, mint);

    expect(minted).toBe(2);
    expect([first, second]).toEqual(["token-1", "token-2"]);
  });

  it("asks for no token on floci, which checks none", async () => {
    let minted = 0;

    const token = await accessToken("http://127.0.0.1:4588", async () => {
      minted++;
      return "token";
    });

    expect(token).toBeUndefined();
    expect(minted).toBe(0);
  });
});

const web = {
  name: "projects/floci-local/locations/europe-west1/services/ocel-j-1-deploy-node-prod-web-a1b2c3",
  uri: "http://ocel-j-1-deploy-node-prod-web-a1b2c3-9f8.europe-west1.run.localhost.floci.io:4588",
};

const api = {
  name: "projects/floci-local/locations/europe-west1/services/ocel-j-1-deploy-node-prod-web-api-d4e5f6",
  uri: "http://ocel-j-1-deploy-node-prod-web-api-d4e5f6-9f8.europe-west1.run.localhost.floci.io:4588",
};

const labelled = (project: string, namespace = "ocel-nightly") => ({
  [NAMESPACE_LABEL]: namespace,
  [PROJECT_LABEL]: project,
});

describe("servicesIn", () => {
  it("reads a service down to the name Cloud Run knows it by and the labels it carries", () => {
    expect(servicesIn({ services: [{ ...web, labels: labelled("j-1-deploy-node") }] })).toEqual([
      {
        name: "ocel-j-1-deploy-node-prod-web-a1b2c3",
        uri: web.uri,
        labels: labelled("j-1-deploy-node"),
      },
    ]);
  });

  it("reads a service that carries no label as one labelled with nothing", () => {
    expect(servicesIn({ services: [web] })[0]?.labels).toEqual({});
  });

  it("reads the empty body a project with no service answers with", () => {
    expect(servicesIn({})).toEqual([]);
  });
});

describe("findAppService", () => {
  const l = (project: string, app?: string, environment = "prod", tier = "production") => ({
    [NAMESPACE_LABEL]: "ocel-nightly",
    [PROJECT_LABEL]: project,
    [ENVIRONMENT_LABEL]: environment,
    [TIER_LABEL]: tier,
    ...(app ? { [APP_LABEL]: app } : {}),
  });
  const service = (name: string, labels: Record<string, string>, uri = `https://${name}`) => ({
    name: `projects/p/locations/l/services/${name}`,
    uri,
    labels,
  });
  const find = (services: unknown[], project = "j-1-deploy-node", app = "web") =>
    findAppService(servicesIn({ services }), "ocel-nightly", project, app);

  it("finds the one service labelled with the app in the project", () => {
    const found = find([
      service("a", l("j-1-deploy-node", "web")),
      service("w", l("j-1-deploy-node")),
    ]);

    expect(found.name).toBe("a");
    expect(found.uri).toBe("https://a");
  });

  it("refuses to pick when two services carry the same app label", () => {
    expect(() =>
      find([service("a", l("j-1-deploy-node", "web")), service("b", l("j-1-deploy-node", "web"))]),
    ).toThrow(/a and b are all labelled ocel-app=web/);
  });

  it("says which services the project has when none carries the app label", () => {
    expect(() =>
      find([service("w", l("j-1-deploy-node")), service("x", l("j-2-deploy-node", "web"))]),
    ).toThrow(/no Cloud Run service is labelled ocel-app=web in project j-1-deploy-node.*\(w\)/);
  });

  it("never answers another project's service of the same app", () => {
    let thrown: unknown;
    try {
      find([service("x", l("j-2-deploy-node", "web"))]);
    } catch (error) {
      thrown = error;
    }

    expect(String(thrown)).toMatch(/no Cloud Run service is labelled ocel-app=web/);
    expect(String(thrown)).not.toContain("(x)");
  });

  it("never answers a preview's service for the production deploy", () => {
    expect(() => find([service("p", l("j-1-deploy-node", "web", "pr-3"))])).toThrow(
      /no Cloud Run service is labelled ocel-app=web/,
    );
  });

  it("never answers a preview's service of the same app", () => {
    expect(() => find([service("p", l("j-1-deploy-node", "web", "prod", "preview"))])).toThrow(
      /no Cloud Run service is labelled ocel-app=web/,
    );
  });

  it("does not let a preview's service of the same app make the production lookup ambiguous", () => {
    const found = find([
      service("a", l("j-1-deploy-node", "web")),
      service("p", l("j-1-deploy-node", "web", "pr-3")),
    ]);

    expect(found.name).toBe("a");
  });

  it("refuses a service that answers on no url of its own", () => {
    expect(() => find([service("a", l("j-1-deploy-node", "web"), "")])).toThrow(
      /answers on no url of its own/,
    );
  });

  it("matches an app as the provider sanitizes it into a label", () => {
    const found = find([service("a", l("j-1-deploy-node", "my-app"))], "j-1-deploy-node", "My App");

    expect(found.name).toBe("a");
  });
});

describe("the services the sweep finds of a project", () => {
  const services = servicesIn({
    services: [
      { name: "a/worker-1", labels: labelled("j-1-deploy-node") },
      { name: "a/checkout-2", labels: labelled("j-1-deploy-node") },
      { name: "a/web-3", labels: labelled("j-1-deploy-node", "ocel") },
      { name: "a/web-4", labels: labelled("j-1-deploy-next") },
      { name: "a/web-5" },
    ],
  });

  it("is every service labelled with the project, whatever the provider named it", () => {
    expect(servicesOf(services, "ocel-nightly", "j-1-deploy-node").map((s) => s.name)).toEqual([
      "worker-1",
      "checkout-2",
    ]);
  });

  it("matches the project and namespace as the provider sanitizes them into a label", () => {
    expect(servicesOf(services, "Ocel Nightly", "J-1/Deploy Node")).toHaveLength(2);
  });

  it("matches a project whose slug is longer than a label holds by the label the provider fitted it to", () => {
    const slug = "ocel-e2e-j-1-deploy-node-with-a-project-slug-long-enough-to-pass-the-label-limit";
    const fitted = "ocel-e2e-j-1-deploy-node-with-a-project-slug-long-enoug-75c491";
    expect(labelValue(slug)).toBe(fitted);
    const long = servicesIn({ services: [{ name: "a/long-1", labels: labelled(fitted) }] });
    expect(servicesOf(long, "ocel-nightly", slug).map((s) => s.name)).toEqual(["long-1"]);
  });

  it("is nothing once the project has no service left", () => {
    expect(servicesOf(services, "ocel-nightly", "j-9-deploy-node")).toEqual([]);
  });
});

describe("strayServices", () => {
  const services = servicesIn({
    services: [
      { name: "a/mine-web", labels: labelled("j-1-deploy-node") },
      { name: "a/mine-worker", labels: labelled("j-1-deploy-node") },
      { name: "a/dead-run", labels: labelled("j-2-deploy-node") },
      { name: "a/someone-elses", labels: labelled("orders") },
      { name: "a/other-namespace", labels: labelled("j-2-deploy-node", "ocel") },
      { name: "a/unlabelled" },
    ],
  });

  it("is every service a run that died left labelled under this namespace", () => {
    expect(strayServices(services, "ocel-nightly", ["j-1-deploy-node"])).toEqual(["dead-run"]);
  });

  it("tells a project apart from one whose slug its own is a prefix of", () => {
    const longer = servicesIn({
      services: [{ name: "a/longer", labels: labelled("j-1-deploy-node-container") }],
    });
    expect(strayServices(longer, "ocel-nightly", ["j-1-deploy-node"])).toEqual(["longer"]);
  });
});

describe("reachable", () => {
  it("is the url itself where the services are real", () => {
    expect(reachable("https://web-123.europe-west1.run.app", undefined)).toBe(
      "https://web-123.europe-west1.run.app",
    );
  });

  it("moves to the port the emulator publishes, which is not the one it serves on inside", () => {
    expect(reachable(web.uri, "http://127.0.0.1:33104")).toBe(
      "http://ocel-j-1-deploy-node-prod-web-a1b2c3-9f8.europe-west1.run.localhost.floci.io:33104",
    );
  });
});

describe("revisionTagUrlsOf", () => {
  const tagged = {
    name: "projects/p/locations/europe-west1/services/ocel-prod-web",
    trafficStatuses: [
      {
        type: "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
        revision: "ocel-prod-web-00002",
        percent: 100,
      },
      {
        revision: "ocel-prod-web-00002",
        tag: "r2b3c4d5e",
        uri: "https://r2b3c4d5e---ocel-prod-web-abc123-ew.a.run.app",
      },
      {
        revision: "ocel-prod-web-00001",
        tag: "r1a2b3c4d",
        uri: "https://r1a2b3c4d---ocel-prod-web-abc123-ew.a.run.app",
      },
    ],
  };
  const other = {
    name: "projects/p/locations/europe-west1/services/ocel-prod-api",
    trafficStatuses: [{ tag: "r9", uri: "https://r9---ocel-prod-api-abc123-ew.a.run.app" }],
  };

  it("is the url of every tag the named service carries, and of no other service", () => {
    expect(revisionTagUrlsOf({ services: [other, tagged] }, "ocel-prod-web")).toEqual([
      "https://r2b3c4d5e---ocel-prod-web-abc123-ew.a.run.app",
      "https://r1a2b3c4d---ocel-prod-web-abc123-ew.a.run.app",
    ]);
  });

  it("is empty for a service that carries no tag", () => {
    expect(
      revisionTagUrlsOf(
        { services: [{ name: tagged.name, trafficStatuses: [tagged.trafficStatuses[0]] }] },
        "ocel-prod-web",
      ),
    ).toEqual([]);
  });
});

describe("exposedServices", () => {
  it("is every field Cloud Run holds of every service the cell's project labels, and none of another's", () => {
    const held = {
      ...web,
      labels: labelled("j-1-deploy-node"),
      template: { containers: [{ env: [{ name: "OCEL_LIVE", value: "manifest" }] }] },
    };
    const worker = { ...api, labels: labelled("j-1-deploy-node") };
    const other = { ...web, labels: labelled("j-2-deploy-node") };

    const exposed = exposedServices(
      { services: [held, worker, other] },
      "ocel-nightly",
      "j-1-deploy-node",
    );

    expect(JSON.parse(exposed)).toEqual([held, worker]);
  });
});

describe("readServices", () => {
  const realFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("holds the services of every page of the listing", async () => {
    const asked: string[] = [];
    globalThis.fetch = (async (input: string | URL | Request) => {
      const url = new URL(String(input));
      asked.push(`${url.pathname}${url.search}`);
      return url.searchParams.get("pageToken") === "next"
        ? Response.json({ services: [{ name: "b" }] })
        : Response.json({ services: [{ name: "a" }], nextPageToken: "next" });
    }) as typeof fetch;

    const read = await readServices({
      endpoint: "http://127.0.0.1:4566",
      project: "p",
      region: "r",
      token: undefined,
    });

    expect(read).toEqual({ services: [{ name: "a" }, { name: "b" }] });
    expect(asked).toEqual([
      "/v2/projects/p/locations/r/services",
      "/v2/projects/p/locations/r/services?pageToken=next",
    ]);
  });
});
