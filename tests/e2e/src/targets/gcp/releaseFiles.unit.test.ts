import { describe, expect, it } from "bun:test";
import { assetPrefixOf, edgeBucketIn, releaseObjectUrl } from "./releaseFiles";

const listed = {
  services: [
    {
      name: "projects/p/locations/r/services/ocel-shop-prod-web",
      template: {
        containers: [
          {
            env: [
              { name: "GREETING", value: "journey-hello" },
              { name: "OCEL_STORAGE_COORDINATE", value: "shop/prod/web/r0123abcd" },
            ],
          },
        ],
      },
    },
    { name: "projects/p/locations/r/services/ocel-shop-prod-api", template: { containers: [{}] } },
  ],
};

describe("the files a release uploaded", () => {
  it("live under the assets of the storage coordinate the service's current template is told", () => {
    expect(assetPrefixOf(listed, "ocel-shop-prod-web")).toBe("assets/shop/prod/web/r0123abcd");
  });

  it("cannot be found for a service that is told no storage coordinate", () => {
    expect(() => assetPrefixOf(listed, "ocel-shop-prod-api")).toThrow(/OCEL_STORAGE_COORDINATE/);
  });

  it("are read from the tier's edge bucket under the release's asset prefix", () => {
    expect(
      releaseObjectUrl(
        "ocel-prod-edge-k3x9q2",
        "assets/shop/prod/web/r0123abcd",
        "/_next/static/chunks/a.js",
      ),
    ).toBe(
      "https://storage.googleapis.com/storage/v1/b/ocel-prod-edge-k3x9q2/o/" +
        "assets%2Fshop%2Fprod%2Fweb%2Fr0123abcd%2F_next%2Fstatic%2Fchunks%2Fa.js?alt=media",
    );
  });
});

describe("the production edge bucket", () => {
  it("is the one bucket named for the namespace's production edge and a random tag", () => {
    expect(
      edgeBucketIn(
        [
          "ocel-prod-edge-k3x9q2",
          "ocel-prod-edge",
          "ocel-prod-edge-k3x9q2-x",
          "ocel-prod-edgy-k3x9q2",
        ],
        "ocel",
      ),
    ).toBe("ocel-prod-edge-k3x9q2");
  });

  it("cannot be told apart where two are listed", () => {
    expect(() => edgeBucketIn(["ocel-prod-edge-k3x9q2", "ocel-prod-edge-a1b2c3"], "ocel")).toThrow(
      /one production edge bucket/,
    );
  });
});
