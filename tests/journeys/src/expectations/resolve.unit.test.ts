import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { contractTitle, UP_TITLE } from "../plan";
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
        gap("one", [{ on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] }], 1),
        gap("two", [{ on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] }]),
      ],
      "dev",
    );
    assert.deepEqual(out["express/web"]?.[UP_TITLE], [
      { id: "one", reason: "reason for one", issue: 1 },
      { id: "two", reason: "reason for two" },
    ]);
  });

  it("lists a test once under a gap whose blocks overlap", () => {
    const out = listed(
      [
        gap("one", [
          { on: ["dev"], cells: ["express/web"], tests: [UP_TITLE] },
          { on: ["dev"], tests: [UP_TITLE] },
        ]),
      ],
      "dev",
    );
    assert.equal(out["express/web"]?.[UP_TITLE]?.length, 1);
    assert.equal(out["hono/web"]?.[UP_TITLE]?.length, 1);
  });

  it("expands a row across the contract legs it names, and the three by default", () => {
    const out = listed(
      [gap("one", [{ on: ["vps"], cells: ["express/web"], tests: [{ row: HEALTH }] }])],
      "vps",
    );
    assert.deepEqual(Object.keys(out["express/web"] ?? {}), [
      HEALTH,
      contractTitle("redeploy", HEALTH),
      contractTitle("rollback", HEALTH),
    ]);
    const named = listed(
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
    const out = listed(
      [
        gap("one", [
          {
            on: ["vps"],
            cells: ["express/web"],
            variants: ["hello"],
            tests: [{ rows: ["static"], legs: ["contract"], except: [SVG] }],
          },
        ]),
      ],
      "vps",
    );
    const titles = Object.keys(out["express-hello/web"] ?? {});
    assert.ok(titles.length > 0);
    assert.ok(!titles.includes(SVG));
    assert.equal(out["express/web"], undefined);
  });

  it("names a cell by its example and app, and reaches every variant of it unless one is named", () => {
    const every = listed([gap("one", [{ on: ["vps"], cells: ["express/web"], tests: [UP_TITLE] }])], "vps");
    assert.ok(every["express/web"]?.[UP_TITLE]);
    assert.ok(every["express-hello/web"]?.[UP_TITLE]);
    const base = listed(
      [gap("one", [{ on: ["vps"], cells: ["express/web"], variants: ["base"], tests: [UP_TITLE] }])],
      "vps",
    );
    assert.ok(base["express/web"]?.[UP_TITLE]);
    assert.equal(base["express-hello/web"], undefined);
  });

  it("leaves a cell alone when its plan has none of the tests named and no cell was named", () => {
    const out = listed(
      [gap("one", [{ on: ["vps"], tests: [{ rows: ["product"], legs: ["contract"] }] }])],
      "vps",
    );
    assert.equal(out["express-hello/web"], undefined);
    assert.ok(out["express/web"]);
  });

  it("refuses a block that names a cell whose plan has none of the tests", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              {
                on: ["vps"],
                cells: ["express/web"],
                variants: ["hello"],
                tests: [{ rows: ["product"] }],
              },
            ]),
          ],
          "vps",
        ),
      /one on vps lists express\/web/,
    );
  });

  it("refuses a block naming a variant the target does not run", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], cells: ["express/web"], variants: ["hello"], tests: [UP_TITLE] }])],
          "dev",
        ),
      /one on dev lists express\/web/,
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
    assert.deepEqual(resolve(gaps, "dev"), { expectations: {}, skipped: {} });
    assert.ok(listed(gaps, "vps")["express/web"]?.[UP_TITLE]);
  });

  it("lists the cells of the variant a block names, and no others", () => {
    const gaps = [gap("one", [{ on: ["aws"], variants: ["cloudflare"], tests: [UP_TITLE] }])];
    const out = listed(gaps, "aws");
    assert.ok(out["express-cloudflare/web"]?.[UP_TITLE]);
    assert.ok(out["with-transforms-cloudflare/web"]?.[UP_TITLE]);
    assert.equal(out["express/web"], undefined);
    assert.equal(out["express-api-gateway/web"], undefined);
  });

  it("names the whole cell a skipping block reaches, under the gap that skips it", () => {
    const { expectations, skipped } = resolve(
      [
        gap("one", [{ on: ["aws"], cells: ["workspace/next"], variants: ["hello"], tests: [UP_TITLE], skip: true }], 9),
        gap("two", [{ on: ["aws"], cells: ["express/web"], tests: [{ row: HEALTH }] }]),
      ],
      "aws",
    );
    assert.deepEqual(skipped, {
      "workspace-hello": [{ id: "one", reason: "reason for one", issue: 9 }],
    });
    assert.ok(expectations["workspace-hello/next"]?.[UP_TITLE]);
    assert.equal(expectations["workspace-hello/express"], undefined);
  });

  it("lists a skipped cell once however many blocks of one gap skip it", () => {
    const { skipped } = resolve(
      [
        gap("one", [
          { on: ["aws"], cells: ["express/web"], tests: [UP_TITLE], skip: true },
          { on: ["aws"], variants: ["base"], tests: [UP_TITLE], skip: true },
        ]),
      ],
      "aws",
    );
    assert.equal(skipped.express?.length, 1);
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
