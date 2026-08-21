import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkHttp: Check = ({ example, baseUrl, runId }) => {
  it("serves health through HTTP", async () => {
    const response = await fetch(`${baseUrl()}/api/health`);
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ ok: true });
  });

  it("preserves the request and response across the serving path", async () => {
    const path = "/api/echo/deep/segment";
    const query = { a: "1", b: "two words", stamp: runId };
    const probe = `probe-${runId}`;
    const body = { stamp: runId, nested: { list: [1, 2, 3] } };
    const target = new URL(path, baseUrl());
    for (const [name, value] of Object.entries(query)) {
      target.searchParams.set(name, value);
    }
    const response = await fetch(target, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-ocel-probe": probe,
      },
      body: JSON.stringify(body),
    });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      framework: example.framework,
      method: "POST",
      path,
      query,
      probeHeader: probe,
      body,
    });

    const status = await fetch(`${baseUrl()}/api/status/418`, {
      redirect: "manual",
    });
    expect(status.status).toBe(418);
  });
};
