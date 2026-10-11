import { describe, expect, it } from "bun:test";
import { assetPrefixOf, releaseObjectUrl, tierBucket } from "./releaseFiles";

const listed = {
  services: [
    {
      name: "projects/p/locations/r/services/ocel-shop-prod-web",
      template: {
        containers: [
          {
            env: [
              { name: "GREETING", value: "journey-hello" },
              { name: "OCEL_ASSET_PREFIX", value: "prod/shop/web/r0123abcd/assets" },
            ],
          },
        ],
      },
    },
    { name: "projects/p/locations/r/services/ocel-shop-prod-api", template: { containers: [{}] } },
  ],
};

describe("the files a release uploaded", () => {
  it("live under the asset prefix the service's current template is told", () => {
    expect(assetPrefixOf(listed, "ocel-shop-prod-web")).toBe("prod/shop/web/r0123abcd/assets");
  });

  it("cannot be found for a service that is told no asset prefix", () => {
    expect(() => assetPrefixOf(listed, "ocel-shop-prod-api")).toThrow(/OCEL_ASSET_PREFIX/);
  });

  it("are read from the tier's bucket under the assets store", () => {
    expect(
      releaseObjectUrl(
        tierBucket("ocel", "acme-prod"),
        "prod/shop/web/r0123abcd/assets",
        "/_next/static/chunks/a.js",
      ),
    ).toBe(
      "https://storage.googleapis.com/storage/v1/b/ocel-acme-prod-production/o/" +
        "assets%2Fprod%2Fshop%2Fweb%2Fr0123abcd%2Fassets%2F_next%2Fstatic%2Fchunks%2Fa.js?alt=media",
    );
  });
});
