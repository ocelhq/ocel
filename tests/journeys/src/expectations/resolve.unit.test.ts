import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { contractTitle, UP_TITLE } from "../plan";
import { resolve } from "./index";
import type { Compute, Edge, Gap } from "./types";

const HEALTH = "GET /health answers with the app name";
const SVG = "GET /ocel.svg serves the svg at its known length";

function gap(id: string, affects: Gap["affects"], issue?: number): Gap {
  return issue === undefined
    ? { id, reason: `reason for ${id}`, affects }
    : { id, reason: `reason for ${id}`, issue, affects };
}

describe("resolve", () => {
  it("lists a test under every gap that names it", () => {
    const listed = resolve(
      [
        gap("one", [{ on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] }], 1),
        gap("two", [{ on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] }]),
      ],
      "dev",
    );
    assert.deepEqual(listed["express/web"]?.[UP_TITLE], [
      { id: "one", reason: "reason for one", issue: 1 },
      { id: "two", reason: "reason for two" },
    ]);
  });

  it("lists a test once under a gap whose blocks overlap", () => {
    const listed = resolve(
      [
        gap("one", [
          { on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] },
          { on: ["dev"], tests: [UP_TITLE] },
        ]),
      ],
      "dev",
    );
    assert.equal(listed["express/web"]?.[UP_TITLE]?.length, 1);
    assert.equal(listed["hono/web"]?.[UP_TITLE]?.length, 1);
  });

  it("expands a row across the contract legs it names, and the three by default", () => {
    const listed = resolve(
      [gap("one", [{ on: ["vps"], cells: ["express/web"], tests: [{ row: HEALTH }] }])],
      "vps",
    );
    assert.deepEqual(Object.keys(listed["express/web"] ?? {}), [
      HEALTH,
      contractTitle("redeploy", HEALTH),
      contractTitle("rollback", HEALTH),
    ]);
    const named = resolve(
      [
        gap("two", [
          { on: ["vps"], cells: ["express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
        ]),
      ],
      "vps",
    );
    assert.deepEqual(Object.keys(named["express/web"] ?? {}), [contractTitle("rollback", HEALTH)]);
  });

  it("expands a suite to its rows, minus the titles it excepts", () => {
    const listed = resolve(
      [
        gap("one", [
          {
            on: ["vps"],
            cells: ["express-hello/web"],
            tests: [{ rows: ["static"], legs: ["contract"], except: [SVG] }],
          },
        ]),
      ],
      "vps",
    );
    const titles = Object.keys(listed["express-hello/web"] ?? {});
    assert.ok(titles.length > 0);
    assert.ok(!titles.includes(SVG));
  });

  it("leaves a cell alone when its plan has none of the tests named and no cell was named", () => {
    const listed = resolve(
      [gap("one", [{ on: ["vps"], tests: [{ rows: ["product"], legs: ["contract"] }] }])],
      "vps",
    );
    assert.equal(listed["express-hello/web"], undefined);
    assert.ok(listed["express/web"]);
  });

  it("refuses a block that names a cell whose plan has none of the tests", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["vps"], cells: ["express-hello/web"], tests: [{ rows: ["product"] }] },
            ]),
          ],
          "vps",
        ),
      /one on vps lists express-hello\/web/,
    );
  });

  it("refuses a block naming a cell in a mode the target does not offer", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], cells: ["express-hello/web"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /express-hello\/web/,
    );
  });

  it("refuses a block naming a leg the target does not run", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["dev"], cells: ["express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
            ]),
          ],
          "dev",
        ),
      /one on dev lists express\/web/,
    );
  });

  it("refuses a block that names a title no cell plans", () => {
    assert.throws(
      () => resolve([gap("one", [{ on: ["dev"], tests: ["not a test"] }])], "dev"),
      /one on dev lists nothing that is planned/,
    );
  });

  it("refuses a block that names a cell the target never runs", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], cells: ["with-transforms/web"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /with-transforms\/web/,
    );
  });

  it("skips a block listed for another environment", () => {
    const gaps = [gap("one", [{ on: ["vps"], tests: [UP_TITLE] }])];
    assert.deepEqual(resolve(gaps, "dev"), {});
    assert.ok(resolve(gaps, "vps")["express/web"]?.[UP_TITLE]);
  });

  it("lists the cells of the edge a block names, and no others", () => {
    const gaps = [gap("one", [{ on: ["aws"], edge: ["cloudflare"], tests: [UP_TITLE] }])];
    const listed = resolve(gaps, "aws");
    assert.ok(listed["express-cloudflare/web"]?.[UP_TITLE]);
    assert.ok(listed["express-hello-cloudflare-container/web"]?.[UP_TITLE]);
    assert.equal(listed["express/web"], undefined);
    assert.equal(listed["express-api-gateway/web"], undefined);
  });

  it("skips a block listed for another compute", () => {
    const gaps = [gap("one", [{ on: ["aws"], compute: ["container"], tests: [UP_TITLE] }])];
    const listed = resolve(gaps, "aws");
    assert.ok(listed["express-container/web"]?.[UP_TITLE]);
    assert.ok(listed["express-hello-container/web"]?.[UP_TITLE]);
    assert.equal(listed["express/web"], undefined);
    assert.equal(listed["express-hello/web"], undefined);
  });

  it("refuses a block naming an edge the target does not run", () => {
    assert.throws(
      () => resolve([gap("one", [{ on: ["vps"], edge: ["cloudfront"], tests: [UP_TITLE] }])], "vps"),
      /one on vps lists cloudfront, which plans none of the tests named/,
    );
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["aws"], edge: ["cloudflare", "fastly" as Edge], tests: [UP_TITLE] },
            ]),
          ],
          "aws",
        ),
      /one on aws lists fastly, which plans none of the tests named/,
    );
  });

  it("refuses a block naming a compute the target does not run", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], compute: ["container"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /one on dev lists container, which plans none of the tests named/,
    );
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["aws"], compute: ["container", "nowhere" as Compute], tests: [UP_TITLE] },
            ]),
          ],
          "aws",
        ),
      /one on aws lists nowhere, which plans none of the tests named/,
    );
  });

  it("refuses two gaps with one id, and a gap that affects nothing", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], tests: [UP_TITLE] }]), gap("one", [])],
          "dev",
        ),
      /one is listed twice/,
    );
    assert.throws(() => resolve([gap("one", [])], "dev"), /one affects nothing/);
  });
});
