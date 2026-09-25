import type { Stored } from "@console/connectors";
import type { DeploymentTopology } from "@console/db/schema";
import { describe, expect, it } from "vitest";
import { matrixOf, stateOf } from "./variables";

const app = (
  name: string,
  folder: string | undefined,
  variables: DeploymentTopology["apps"][number]["variables"],
): DeploymentTopology["apps"][number] => ({
  name,
  ...(folder !== undefined && { folder }),
  runtime: { name: "nodejs22.x" },
  compute: "serverless",
  urls: [],
  hostnames: [],
  outcome: "succeeded",
  variables,
});

const topology = (apps: DeploymentTopology["apps"]): DeploymentTopology => ({
  apps,
  resources: [],
  usages: [],
});

const stored = (key: string, folder = "", environment = ""): Stored => ({
  key,
  folder,
  environment,
  version: 1,
  updatedAt: 0,
  size: 1,
});

const cellAt = (matrix: ReturnType<typeof matrixOf>, key: string, folder: string) =>
  matrix.rows.find((row) => row.key === key)?.cells.find((cell) => cell.folder === folder);

describe("matrixOf", () => {
  it("reads a key an app in a folder declares from root, because root is the inheritance base", () => {
    const matrix = matrixOf(
      topology([app("web", "/apps/web", [{ key: "API", class: "plain", required: true }])]),
      [],
      [],
    );
    expect(cellAt(matrix, "API", "")?.state).toBe("required");
    expect(cellAt(matrix, "API", "/apps/web")?.state).toBe("optional");
    expect(matrix.rows.find((row) => row.key === "API")?.scope).toBeUndefined();
  });

  it("forbids root for a key the declaration explicitly scoped to a folder", () => {
    const matrix = matrixOf(
      topology([
        app("web", "/apps/web", [
          { key: "API", class: "plain", required: true, folder: "/apps/web" },
        ]),
      ]),
      [],
      [],
    );
    expect(cellAt(matrix, "API", "")?.state).toBe("forbidden");
    expect(cellAt(matrix, "API", "/apps/web")?.state).toBe("required");
    expect(matrix.rows.find((row) => row.key === "API")?.scope).toEqual(["/apps/web"]);
  });

  it("offers every folder as an override point for an unscoped key, as the CLI does", () => {
    const matrix = matrixOf(
      topology([
        app("web", "/apps/web", [{ key: "API", class: "plain", required: true }]),
        app("api", "/apps/api", [{ key: "OTHER", class: "plain", required: true }]),
      ]),
      [],
      [],
    );
    expect(cellAt(matrix, "API", "/apps/api")?.state).toBe("optional");
  });

  it("marks a stored value set at the folder it was stored at, not at root", () => {
    const matrix = matrixOf(
      topology([app("web", "/apps/web", [{ key: "API", class: "plain", required: true }])]),
      [stored("API", "/apps/web")],
      [],
    );
    expect(cellAt(matrix, "API", "")?.set).toBe(false);
    expect(cellAt(matrix, "API", "/apps/web")?.set).toBe(true);
  });

  it("keeps a stored key nothing declares, so an orphan is visible rather than hidden", () => {
    const matrix = matrixOf(
      topology([app("web", undefined, [{ key: "API", class: "plain", required: true }])]),
      [stored("GONE")],
      [],
    );
    const orphan = matrix.rows.find((row) => row.key === "GONE");
    expect(orphan).toBeDefined();
    expect(orphan?.cells.every((cell) => cell.state === "forbidden")).toBe(true);
    expect(cellAt(matrix, "GONE", "")?.set).toBe(true);
  });

  it("flags an override whose environment no longer exists", () => {
    const matrix = matrixOf(
      topology([app("web", undefined, [{ key: "API", class: "plain", required: true }])]),
      [stored("API", "", "pr-9")],
      ["pr-1"],
    );
    expect(cellAt(matrix, "API", "")?.overrides).toEqual([
      { environment: "pr-9", version: 1, orphaned: true },
    ]);
  });

  it("leaves a derived variable out, because nothing stores one", () => {
    const matrix = matrixOf(
      topology([app("web", undefined, [{ key: "BUCKET", class: "derived" }])]),
      [],
      [],
    );
    expect(matrix.rows).toHaveLength(0);
  });
});

describe("stateOf", () => {
  const held = topology([app("web", undefined, [{ key: "API", class: "plain", required: true }])]);

  it("carries the caller's ability onto the state the table renders", () => {
    expect(
      stateOf("acme", "production", held, [], [], { write: true, reveal: false }),
    ).toMatchObject({ can: { write: true, reveal: false }, values: "live" });
  });

  it("names no ability the caller does not hold, and stays unknown when nothing was read", () => {
    expect(
      stateOf("acme", "preview", held, [], [], { write: false, reveal: false }, "unknown"),
    ).toMatchObject({ can: { write: false, reveal: false }, values: "unknown" });
  });
});

describe("an env source", () => {
  const held = topology([app("web", undefined, [{ key: "API", class: "plain", required: true }])]);
  const infisical = {
    id: "infisical:p-1/prod",
    writable: false,
    links: { "": "https://infisical.example/root" },
    credentials: ["INFISICAL_CLIENT_ID"],
  };

  it("names the source a stored value was read from", () => {
    const matrix = matrixOf(held, [{ ...stored("API"), envSource: infisical.id }], [], infisical);
    expect(cellAt(matrix, "API", "")?.envSource).toBe(infisical.id);
  });

  it("lists what the source holds and nothing declares as drift, not as a row", () => {
    const matrix = matrixOf(
      held,
      [{ ...stored("RETIRED"), envSource: infisical.id }, stored("GONE")],
      [],
      infisical,
    );
    expect(matrix.drift).toEqual([{ key: "RETIRED", folder: "" }]);
    expect(matrix.rows.map((row) => row.key)).toEqual(["API", "GONE", "INFISICAL_CLIENT_ID"]);
  });

  it("offers the credentials it signs in with as secrets in the env source group", () => {
    const matrix = matrixOf(held, [], [], infisical);
    expect(matrix.rows.find((row) => row.key === "INFISICAL_CLIENT_ID")).toMatchObject({
      class: "secret",
      group: "env source",
      cells: [{ folder: "", state: "required", set: false }],
    });
    expect(matrix.groups).toContainEqual({
      key: "env source",
      required: true,
      description: "how ocel signs in to infisical:p-1/prod",
    });
  });

  it("rides on the state the table renders", () => {
    expect(
      stateOf("acme", "production", held, [], [], { write: true, reveal: true }, "live", infisical),
    ).toMatchObject({ envSource: infisical });
  });
});
