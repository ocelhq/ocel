import { afterEach, describe, expect, it } from "bun:test";
import {
  databaseFilter,
  databasesIn,
  deleteDatabase,
  listDatabases,
  strayDatabases,
} from "./cloudsql";
import type { Where } from "./store";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

const where: Where = { endpoint: undefined, project: "p", region: "r", token: "t" };

function sqlAdminAnswering(answer: (url: URL, method: string) => Response): string[] {
  const asked: string[] = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    asked.push(`${method} ${url.pathname}${url.search}`);
    return answer(url, method);
  }) as typeof fetch;
  return asked;
}

describe("databaseFilter", () => {
  it("names the namespace label the provider puts on its instances", () => {
    expect(databaseFilter("Ocel")).toBe("settings.userLabels.ocel-namespace:ocel");
  });
});

describe("databasesIn", () => {
  it("reads each instance's name and the project it was labelled with", () => {
    expect(
      databasesIn({
        items: [
          {
            name: "ocel--j-1-prod-orders-a1b2c3",
            settings: { userLabels: { "ocel-project": "j-1" } },
          },
        ],
      }),
    ).toEqual([{ name: "ocel--j-1-prod-orders-a1b2c3", project: "j-1" }]);
    expect(databasesIn({})).toEqual([]);
  });
});

describe("strayDatabases", () => {
  it("is what a run that died left under the harness's projects", () => {
    const databases = [
      { name: "a", project: "j-1-sdk-node" },
      { name: "b", project: "j-2-sdk-node" },
      { name: "c", project: "customer-shop" },
    ];
    expect(strayDatabases(databases, ["j-2-sdk-node"])).toEqual(["a"]);
  });
});

describe("listDatabases", () => {
  it("reads every page of the listing, filtering each one", async () => {
    const asked = sqlAdminAnswering((url) =>
      url.searchParams.get("pageToken") === "next"
        ? Response.json({
            items: [{ name: "b", settings: { userLabels: { "ocel-project": "j-2" } } }],
          })
        : Response.json({
            items: [{ name: "a", settings: { userLabels: { "ocel-project": "j-1" } } }],
            nextPageToken: "next",
          }),
    );

    expect(await listDatabases(where, "settings.userLabels.ocel-namespace:ocel")).toEqual([
      { name: "a", project: "j-1" },
      { name: "b", project: "j-2" },
    ]);
    const filtered = `filter=${encodeURIComponent("settings.userLabels.ocel-namespace:ocel")}`;
    expect(asked).toEqual([
      `GET /v1/projects/p/instances?${filtered}`,
      `GET /v1/projects/p/instances?${filtered}&pageToken=next`,
    ]);
  });
});

describe("deleteDatabase", () => {
  it("returns once the delete operation has finished", async () => {
    const asked = sqlAdminAnswering(() => Response.json({ name: "op-1", status: "DONE" }));

    await deleteDatabase(where, "ocel--j-1-orders-a1b2c3");
    expect(asked).toEqual(["DELETE /v1/projects/p/instances/ocel--j-1-orders-a1b2c3"]);
  });

  it("fails naming the instance when the delete operation fails", async () => {
    sqlAdminAnswering(() =>
      Response.json({ name: "op-1", status: "DONE", error: { errors: [{ message: "busy" }] } }),
    );

    await expect(deleteDatabase(where, "ocel--a")).rejects.toThrow("deleting ocel--a failed: busy");
  });

  it("is done when the instance is already gone", async () => {
    sqlAdminAnswering(() => Response.json({}, { status: 404 }));

    await deleteDatabase(where, "ocel--a");
  });
});
