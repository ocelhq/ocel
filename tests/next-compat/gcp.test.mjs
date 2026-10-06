import { describe, expect, it } from "vitest";

import { listProjectSlugs } from "./gcp.mjs";

function serving(pages) {
  const asked = [];
  const fetch = async (url, init) => {
    asked.push({ url: String(url), authorization: init?.headers?.authorization });
    const token = new URL(String(url)).searchParams.get("pageToken") ?? "";
    const page = pages[token];
    if (!page) {
      return new Response("nothing here", { status: 403 });
    }
    return Response.json(page);
  };
  return { fetch, asked };
}

const service = (project, namespace = "e2e-nc") => ({
  name: `projects/p/locations/r/services/s-${project}`,
  labels: { "ocel-namespace": namespace, "ocel-project": project },
});

describe("listProjectSlugs", () => {
  it("lists the project slugs a namespace's Cloud Run services are labelled with, across every page", async () => {
    const { fetch } = serving({
      "": { services: [service("e2e-9-bbbb"), service("e2e-1")], nextPageToken: "two" },
      two: { services: [service("e2e-9-bbbb"), service("e2e-5")] },
    });

    const slugs = await listProjectSlugs({
      project: "p",
      region: "r",
      namespace: "e2e-nc",
      token: "t0ken",
      fetch,
    });

    expect(slugs).toEqual(["e2e-1", "e2e-5", "e2e-9-bbbb"]);
  });

  it("leaves out services another namespace labelled, and services with no project label", async () => {
    const { fetch } = serving({
      "": {
        services: [
          service("e2e-1"),
          service("e2e-2", "ocel"),
          { name: "bare", labels: { "ocel-namespace": "e2e-nc" } },
          { name: "unlabelled" },
        ],
      },
    });

    expect(
      await listProjectSlugs({ project: "p", region: "r", namespace: "e2e-nc", token: "t", fetch }),
    ).toEqual(["e2e-1"]);
  });

  it("asks Cloud Run with the access token it was given", async () => {
    const { fetch, asked } = serving({ "": { services: [] } });

    await listProjectSlugs({
      project: "p",
      region: "r",
      namespace: "e2e-nc",
      token: "t0ken",
      fetch,
    });

    expect(asked).toEqual([
      {
        url: "https://run.googleapis.com/v2/projects/p/locations/r/services",
        authorization: "Bearer t0ken",
      },
    ]);
  });

  it("names the url and status when Cloud Run refuses the listing", async () => {
    const { fetch } = serving({});

    await expect(
      listProjectSlugs({ project: "p", region: "r", namespace: "e2e-nc", token: "t", fetch }),
    ).rejects.toThrow(
      /GET https:\/\/run\.googleapis\.com\/v2\/projects\/p\/locations\/r\/services = 403 nothing here/,
    );
  });
});
