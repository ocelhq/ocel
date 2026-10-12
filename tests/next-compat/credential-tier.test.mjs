import { describe, expect, it } from "vitest";

import { awsPolicyDocument } from "./credential-tier.mjs";

const AWS = {
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Action: "s3:*", Resource: "*" }],
};

const PRODUCTION_AWS = {
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Action: "cloudformation:DescribeStacks", Resource: "production" }],
};

describe("awsPolicyDocument", () => {
  it("takes the preview tier's IAM document out of one AWS group per tier beside a cloudflare group", () => {
    const stdout = JSON.stringify({
      ok: true,
      data: {
        groups: [
          { heading: "AWS credentials for the production tier", document: PRODUCTION_AWS },
          { heading: "AWS credentials for the preview tier", document: AWS },
          { heading: "Cloudflare API token", document: "Workers Scripts: Edit\nDNS: Edit" },
        ],
      },
    });

    expect(JSON.parse(awsPolicyDocument(stdout))).toEqual(AWS);
  });

  it("refuses output that holds an IAM document for no preview tier", () => {
    const stdout = JSON.stringify({
      ok: true,
      data: {
        groups: [{ heading: "AWS credentials for the production tier", document: PRODUCTION_AWS }],
      },
    });

    expect(() => awsPolicyDocument(stdout)).toThrow(/no IAM policy document/);
  });

  it("takes the IAM document out of a cloudflare edge's two permission groups", () => {
    const stdout = JSON.stringify({
      ok: true,
      data: {
        groups: [
          { heading: "AWS credentials for the preview tier", document: AWS },
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
