import { describe, expect, it } from "vitest";

import { awsPolicyDocument } from "./credential-tier.mjs";

const AWS = {
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Action: "s3:*", Resource: "*" }],
};

describe("awsPolicyDocument", () => {
  it("takes the IAM document out of a cloudflare edge's two permission groups", () => {
    const stdout = JSON.stringify({
      ok: true,
      data: {
        groups: [
          { heading: "AWS IAM policy", document: AWS },
          { heading: "Cloudflare API token", document: "Workers Scripts: Edit\nDNS: Edit" },
        ],
      },
    });

    expect(JSON.parse(awsPolicyDocument(stdout))).toEqual(AWS);
  });

  it("refuses output that holds no IAM document", () => {
    const stdout = JSON.stringify({
      ok: true,
      data: { groups: [{ heading: "Cloudflare API token", document: "DNS: Edit" }] },
    });

    expect(() => awsPolicyDocument(stdout)).toThrow(/no IAM policy document/);
  });
});
