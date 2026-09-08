import { describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import { BOOTSTRAP_APIS, reachable, servedBy, servicesIn, standing } from "./store";

describe("the apis a bootstrap wants on", () => {
  it("are the ones the provider refuses without", async () => {
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "bootstrapitems.go"),
      "utf8",
    );
    const listed = go.match(/var BootstrapAPIs = \[\]string\{([^}]*)\}/);
    expect(listed).not.toBeNull();
    const declared = [...(listed?.[1] ?? "").matchAll(/"([^"]+)"/g)].map((one) => one[1]);
    expect(declared).toEqual(BOOTSTRAP_APIS);
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

describe("servicesIn", () => {
  it("reads a service down to the name Cloud Run knows it by", () => {
    expect(servicesIn({ services: [web] })).toEqual([
      { name: "ocel-j-1-deploy-node-prod-web-a1b2c3", uri: web.uri },
    ]);
  });

  it("reads the empty body a project with no service answers with", () => {
    expect(servicesIn({})).toEqual([]);
  });
});

describe("servedBy", () => {
  it("is the url of the service named for the app itself, not one named for a function under it", () => {
    expect(servedBy(servicesIn({ services: [api, web] }), "ocel-j-1-deploy-node-prod-web")).toBe(
      web.uri,
    );
  });

  it("tells an app apart from one whose name it is a prefix of", () => {
    const other = {
      name: "projects/p/locations/l/services/ocel-j-1-deploy-node-prod-website-a1b2c3",
      uri: "http://website",
    };
    expect(() =>
      servedBy(servicesIn({ services: [other] }), "ocel-j-1-deploy-node-prod-web"),
    ).toThrow(/no Cloud Run service/);
  });
});

describe("standing", () => {
  it("holds while any service of the project stands", () => {
    const services = servicesIn({ services: [web, api] });
    expect(standing(services, ["ocel-j-1-deploy-node-prod-web"])).toBe(true);
    expect(standing(services, ["ocel-j-1-deploy-next-prod-web"])).toBe(false);
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
