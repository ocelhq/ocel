import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { contractTitle, UP_TITLE } from "../plan";
import { EDGE_ENV, expectationsFor, resolve } from "./index";
import type { Gap } from "./types";

const before = process.env[EDGE_ENV];

afterEach(() => {
  if (before === undefined) {
    delete process.env[EDGE_ENV];
  } else {
    process.env[EDGE_ENV] = before;
  }
});

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
      undefined,
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
      undefined,
    );
    assert.equal(listed["express/web"]?.[UP_TITLE]?.length, 1);
    assert.equal(listed["hono/web"]?.[UP_TITLE]?.length, 1);
  });

  it("expands a row across the contract legs it names, and the three by default", () => {
    const listed = resolve(
      [
        gap("one", [{ on: ["dev"], cells: ["express/web"], tests: [{ row: HEALTH }] }]),
        gap("two", [
          { on: ["vps"], cells: ["express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
        ]),
      ],
      "dev",
      undefined,
    );
    assert.deepEqual(Object.keys(listed["express/web"] ?? {}), [
      HEALTH,
      contractTitle("redeploy", HEALTH),
      contractTitle("rollback", HEALTH),
    ]);
    const vps = resolve(
      [
        gap("two", [
          { on: ["vps"], cells: ["express/web"], tests: [{ row: HEALTH, legs: ["rollback"] }] },
        ]),
      ],
      "vps",
      undefined,
    );
    assert.deepEqual(Object.keys(vps["express/web"] ?? {}), [contractTitle("rollback", HEALTH)]);
  });

  it("expands a suite to its rows, minus the titles it excepts", () => {
    const listed = resolve(
      [
        gap("one", [
          {
            on: ["dev"],
            cells: ["hello-express/web"],
            tests: [{ rows: ["static"], legs: ["contract"], except: [SVG] }],
          },
        ]),
      ],
      "dev",
      undefined,
    );
    const titles = Object.keys(listed["hello-express/web"] ?? {});
    assert.ok(titles.length > 0);
    assert.ok(!titles.includes(SVG));
  });

  it("leaves a cell alone when its plan has none of the tests named and no cell was named", () => {
    const listed = resolve(
      [gap("one", [{ on: ["dev"], tests: [{ rows: ["product"], legs: ["contract"] }] }])],
      "dev",
      undefined,
    );
    assert.equal(listed["hello-express/web"], undefined);
    assert.ok(listed["express/web"]);
  });

  it("refuses a block that names a cell whose plan has none of the tests", () => {
    assert.throws(
      () =>
        resolve(
          [
            gap("one", [
              { on: ["dev"], cells: ["hello-express/web"], tests: [{ rows: ["product"] }] },
            ]),
          ],
          "dev",
          undefined,
        ),
      /one on dev lists hello-express\/web/,
    );
  });

  it("refuses a block that names a title no cell plans", () => {
    assert.throws(
      () => resolve([gap("one", [{ on: ["dev"], tests: ["not a test"] }])], "dev", undefined),
      /one on dev lists nothing that is planned/,
    );
  });

  it("refuses a block that names a cell the target never runs", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], cells: ["with-transforms/web"], tests: [UP_TITLE] }])],
          "dev",
          undefined,
        ),
      /with-transforms\/web/,
    );
  });

  it("skips a block listed for another environment or edge", () => {
    const gaps = [
      gap("one", [
        { on: ["vps"], tests: [UP_TITLE] },
        { on: ["aws"], edge: ["cloudfront"], tests: [UP_TITLE] },
      ]),
    ];
    assert.deepEqual(resolve(gaps, "dev", undefined), {});
    assert.deepEqual(resolve(gaps, "aws", "cloudflare"), {});
    assert.ok(resolve(gaps, "aws", "cloudfront")["express/web"]?.[UP_TITLE]);
  });

  it("refuses two gaps with one id, and a gap that affects nothing", () => {
    assert.throws(
      () =>
        resolve(
          [gap("one", [{ on: ["dev"], tests: [UP_TITLE] }]), gap("one", [])],
          "dev",
          undefined,
        ),
      /one is listed twice/,
    );
    assert.throws(() => resolve([gap("one", [])], "dev", undefined), /one affects nothing/);
  });
});

describe("expectationsFor", () => {
  it("does not read the edge for an environment that has no edges", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.doesNotThrow(() => expectationsFor("dev"));
    assert.doesNotThrow(() => expectationsFor("vps"));
  });

  it("names the variable when an aws environment is asked for an edge no gap lists", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.throws(() => expectationsFor("aws"), new RegExp(EDGE_ENV));
    assert.throws(() => expectationsFor("aws.floci"), new RegExp(EDGE_ENV));
  });
});
