import { afterEach, describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  createTimesIn,
  deleteStore,
  listStores,
  NETWORK_FEATURE,
  storeFilter,
  storesIn,
  strayStores,
} from "./memorystore";
import type { Where } from "./store";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

const where: Where = { endpoint: undefined, project: "p", region: "r", token: "t" };

function memorystoreAnswering(answer: (url: URL, method: string) => Response): string[] {
  const asked: string[] = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    asked.push(`${method} ${url.pathname}${url.search}`);
    return answer(url, method);
  }) as typeof fetch;
  return asked;
}

describe("createTimesIn", () => {
  it("reads each store's create operation and when it ran from what a deploy said", () => {
    const said = [
      "Creating kv cache as Memorystore instance ocel-j-1-kv-node-prod-cache-a1b2c3 in europe-west1: a new instance takes several minutes",
      "Created kv cache: its create operation projects/p/locations/europe-west1/operations/operation-1 created 2026-10-01T10:00:00Z and ended 2026-10-01T10:14:30Z",
      "Releasing a new revision of Cloud Run service ocel-j-1-kv-node-prod-web-d4e5f6 in europe-west1",
    ].join("\n");

    expect(createTimesIn(said)).toEqual([
      {
        store: "cache",
        operation: "projects/p/locations/europe-west1/operations/operation-1",
        createTime: "2026-10-01T10:00:00Z",
        endTime: "2026-10-01T10:14:30Z",
        seconds: 870,
      },
    ]);
  });

  it("is nothing for a deploy that created no store", () => {
    expect(createTimesIn("Releasing a new revision of Cloud Run service web\n")).toEqual([]);
  });

  it("reads the line the provider says", async () => {
    const go = await readFile(path.join(repoRoot, "platform", "gcp", "provider", "kv.go"), "utf8");
    expect(go).toContain('": its create operation " + finished.Name');
    expect(go).toContain('" created " + timed.CreateTime + " and ended " + timed.EndTime');
  });
});

describe("storeFilter", () => {
  it("names the labels the provider puts on a project's stores", async () => {
    expect(storeFilter("ocel", "J-1 kv/node")).toBe(
      'labels.ocel-namespace="ocel" AND labels.ocel-project="j-1-kv-node"',
    );
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "labels.go"),
      "utf8",
    );
    expect(go).toContain("labels := stackLabels(names, ref)");
  });
});

describe("storesIn", () => {
  it("reads each instance's name and the project it was labelled with", () => {
    const listed = {
      instances: [
        {
          name: "projects/p/locations/r/instances/ocel-j-1-kv-node-prod-cache-a1b2c3",
          labels: { "ocel-project": "j-1-kv-node" },
        },
      ],
    };
    expect(storesIn(listed)).toEqual([
      {
        name: "projects/p/locations/r/instances/ocel-j-1-kv-node-prod-cache-a1b2c3",
        project: "j-1-kv-node",
      },
    ]);
    expect(storesIn({})).toEqual([]);
  });
});

describe("strayStores", () => {
  it("is what a run that died left under the harness's projects", () => {
    const stores = [
      { name: "a", project: "j-1-kv-node" },
      { name: "b", project: "j-2-kv-node" },
      { name: "c", project: "customer-shop" },
    ];
    expect(strayStores(stores, ["j-2-kv-node"])).toEqual(["a"]);
  });
});

describe("the private network feature", () => {
  it("is the one the provider installs", async () => {
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "bootstrapfeatures.go"),
      "utf8",
    );
    expect(go).toContain(`networkFeature     = "${NETWORK_FEATURE}"`);
  });
});

describe("listStores", () => {
  it("reads every page of the listing, filtering each one", async () => {
    const filter = 'labels.ocel-namespace="ocel"';
    const asked = memorystoreAnswering((url) =>
      url.searchParams.get("pageToken") === "next"
        ? Response.json({ instances: [{ name: "b", labels: { "ocel-project": "j-2" } }] })
        : Response.json({
            instances: [{ name: "a", labels: { "ocel-project": "j-1" } }],
            nextPageToken: "next",
          }),
    );

    expect(await listStores(where, filter)).toEqual([
      { name: "a", project: "j-1" },
      { name: "b", project: "j-2" },
    ]);
    const filtered = `filter=${encodeURIComponent(filter).replace(/%20/g, "+")}`;
    expect(asked).toEqual([
      `GET /v1/projects/p/locations/r/instances?${filtered}`,
      `GET /v1/projects/p/locations/r/instances?${filtered}&pageToken=next`,
    ]);
  });
});

describe("deleteStore", () => {
  const name = "projects/p/locations/r/instances/ocel-j-1-cache-a1b2c3";

  it("returns once the delete operation has finished", async () => {
    const asked = memorystoreAnswering((_, method) =>
      method === "DELETE"
        ? Response.json({ name: "projects/p/locations/r/operations/op-1", done: true })
        : Response.json({}, { status: 500 }),
    );

    await deleteStore(where, name);
    expect(asked).toEqual([`DELETE /v1/${name}`]);
  });

  it("fails naming the store when the delete operation fails", async () => {
    memorystoreAnswering(() =>
      Response.json({ name: "op-1", done: true, error: { message: "instance is busy" } }),
    );

    await expect(deleteStore(where, name)).rejects.toThrow(
      `deleting ${name} failed: instance is busy`,
    );
  });

  it("is done when the store is already gone", async () => {
    memorystoreAnswering(() => Response.json({ error: { code: 404 } }, { status: 404 }));

    await deleteStore(where, name);
  });
});
