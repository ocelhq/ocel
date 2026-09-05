import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { contractTitle, UP_TITLE } from "../plan";
import { productRows, staticRows } from "../rows";
import { resolve } from "./index";
import type { ExpectationEnvironment, Gap } from "./types";

const HEALTH = "GET /health answers with the app name";
const SVG = "GET /ocel.svg serves the svg at its known length";

function gap(id: string, affects: Gap["affects"], issue?: number): Gap {
  return issue === undefined
    ? { id, reason: `reason for ${id}`, affects }
    : { id, reason: `reason for ${id}`, issue, affects };
}

function listed(gaps: Gap[], environment: ExpectationEnvironment) {
  return resolve(gaps, environment).expectations;
}

describe("resolve", () => {
  it("lists a test under every gap that names it", () => {
    const out = listed(
      [
        gap("one", [{ on: ["dev"], cells: ["sdk/express/web"], tests: [UP_TITLE] }], 1),
        gap("two", [{ on: ["dev"], cells: ["sdk/express/web"], tests: [UP_TITLE] }]),
      ],
      "dev",
    );
    assert.deepEqual(out["sdk/express/web"]?.[UP_TITLE], [
      { id: "one", reason: "reason for one", issue: 1 },
      { id: "two", reason: "reason for two" },
    ]);
  });

  it("lists a test once under a gap whose blocks overlap", () => {
    const out = listed(
      [
        gap("one", [
          { on: ["dev"], cells: ["sdk/express/web"], tests: [UP_TITLE] },
          { on: ["dev"], tests: [UP_TITLE] },
        ]),
      ],
      "dev",
    );
    assert.equal(out["sdk/express/web"]?.[UP_TITLE]?.length, 1);
    assert.equal(out["sdk/hono/web"]?.[UP_TITLE]?.length, 1);
  });

  it("expands a row across the contract legs it names, and the three by default", () => {
    const out = listed(
      [gap("one", [{ on: ["vps"], cells: ["sdk/express/web"], tests: [{ row: HEALTH }] }])],
      "vps",
    );
    assert.deepEqual(Object.keys(out["sdk/express/web"] ?? {}), [
      HEALTH,
      contractTitle("redeploy", HEALTH),
      contractTitle("rollback", HEALTH),
    ]);
    const named = listed(
      [
        gap("two", [
          { on: ["vps"], cells: ["sdk/express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
        ]),
      ],
      "vps",
    );
    assert.deepEqual(Object.keys(named["sdk/express/web"] ?? {}), [contractTitle("rollback", HEALTH)]);
  });

  it("expands a set of rows, minus the titles it excepts", () => {
    const out = listed(
      [
        gap("one", [
          {
            on: ["aws"],
            cells: ["sdk/express/web"],
            variants: ["container"],
            tests: [{ rows: staticRows, legs: ["contract"], except: [SVG] }],
          },
        ]),
      ],
      "aws",
    );
    const titles = Object.keys(out["sdk/express-container/web"] ?? {});
    assert.ok(titles.length > 0);
    assert.ok(!titles.includes(SVG));
    assert.equal(out["sdk/express/web"], undefined);
  });

  it("names a cell by its fixture and app, and reaches every variant of it unless one is named", () => {
    const every = listed([gap("one", [{ on: ["aws"], cells: ["sdk/express/web"], tests: [UP_TITLE] }])], "aws");
    assert.ok(every["sdk/express/web"]?.[UP_TITLE]);
    assert.ok(every["sdk/express-container/web"]?.[UP_TITLE]);
    const base = listed(
      [gap("one", [{ on: ["aws"], cells: ["sdk/express/web"], variants: ["base"], tests: [UP_TITLE] }])],
      "aws",
    );
    assert.ok(base["sdk/express/web"]?.[UP_TITLE]);
    assert.equal(base["sdk/express-container/web"], undefined);
  });

  it("leaves a cell alone when its plan has none of the tests named and no cell was named", () => {
    const out = listed(
      [gap("one", [{ on: ["aws"], tests: [{ rows: productRows, legs: ["contract"] }] }])],
      "aws",
    );
    assert.equal(out["deploy/express/web"], undefined);
    assert.ok(out["sdk/express/web"]);
  });

  it("refuses a block that names a cell whose plan has none of the tests", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              {
                on: ["aws"],
                cells: ["deploy/express/web"],
                tests: [{ rows: productRows }],
              },
            ]),
          ],
          "aws",
        ),
      /one on aws lists deploy\/express\/web/,
    );
  });

  it("refuses a block naming a variant the target does not run", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], cells: ["sdk/express/web"], variants: ["container"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /one on dev lists sdk\/express\/web/,
    );
    assert.throws(
      () => resolve([gap("one", [{ on: ["vps"], variants: ["cloudflare"], tests: [UP_TITLE] }])], "vps"),
      /one on vps lists cloudflare, which plans none of the tests named/,
    );
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["aws"], variants: ["cloudflare", "fastly"], tests: [UP_TITLE] }])],
          "aws",
        ),
      /one on aws lists fastly, which plans none of the tests named/,
    );
  });

  it("refuses a block naming a leg the target does not run", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["dev"], cells: ["sdk/express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
            ]),
          ],
          "dev",
        ),
      /one on dev lists sdk\/express\/web/,
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
          [gap("one", [{ on: ["dev"], cells: ["sdk/with-transforms/web"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /with-transforms\/web/,
    );
  });

  it("skips a block listed for another environment", () => {
    const gaps = [gap("one", [{ on: ["vps"], tests: [UP_TITLE] }])];
    assert.deepEqual(resolve(gaps, "dev"), { expectations: {}, skipped: {} });
    assert.ok(listed(gaps, "vps")["sdk/express/web"]?.[UP_TITLE]);
  });

  it("lists the cells of the variant a block names, and no others", () => {
    const gaps = [gap("one", [{ on: ["aws"], variants: ["cloudflare"], tests: [UP_TITLE] }])];
    const out = listed(gaps, "aws");
    assert.ok(out["sdk/express-cloudflare/web"]?.[UP_TITLE]);
    assert.ok(out["sdk/with-transforms-cloudflare/web"]?.[UP_TITLE]);
    assert.equal(out["sdk/express/web"], undefined);
    assert.equal(out["sdk/express-api-gateway/web"], undefined);
  });

  it("names the whole cell a skipping block reaches, under the gap that skips it", () => {
    const { expectations, skipped } = resolve(
      [
        gap("one", [{ on: ["aws"], cells: ["sdk/workspace/next"], variants: ["container"], tests: [UP_TITLE], skip: true }], 9),
        gap("two", [{ on: ["aws"], cells: ["sdk/express/web"], tests: [{ row: HEALTH }] }]),
      ],
      "aws",
    );
    assert.deepEqual(skipped, {
      "sdk/workspace-container": [{ id: "one", reason: "reason for one", issue: 9 }],
    });
    assert.ok(expectations["sdk/workspace-container/next"]?.[UP_TITLE]);
    assert.equal(expectations["sdk/workspace-container/express"], undefined);
  });

  it("lists a skipped cell once however many blocks of one gap skip it", () => {
    const { skipped } = resolve(
      [
        gap("one", [
          { on: ["aws"], cells: ["sdk/express/web"], tests: [UP_TITLE], skip: true },
          { on: ["aws"], variants: ["base"], tests: [UP_TITLE], skip: true },
        ]),
      ],
      "aws",
    );
    assert.equal(skipped["sdk/express"]?.length, 1);
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
