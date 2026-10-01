import { describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import { createTimesIn, KV_FEATURE, storeFilter, storesIn, strayStores } from "./memorystore";

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
    const go = await readFile(path.join(repoRoot, "platform", "gcp", "provider", "kv.go"), "utf8");
    expect(go).toContain('"ocel-namespace":');
    expect(go).toContain('"ocel-project":');
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

describe("the kv network feature", () => {
  it("is the one the provider installs", async () => {
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "bootstrapfeatures.go"),
      "utf8",
    );
    expect(go).toContain(`kvFeature          = "${KV_FEATURE}"`);
  });
});
