import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { base, nightlyVersion, plan, stale, stamp } from "./nightly.mjs";
import { parse } from "./version.mjs";

const SEMVER =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

function comparePrerelease(a, b) {
  const [x, y] = [a.split("."), b.split(".")];
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const [p, q] = [x[i], y[i]];
    if (p === q) continue;
    const [pn, qn] = [/^\d+$/.test(p), /^\d+$/.test(q)];
    if (pn && qn) return Number(p) - Number(q);
    if (pn) return -1;
    if (qn) return 1;
    return p < q ? -1 : 1;
  }
  return x.length - y.length;
}

const SHA = "abc1234def5678901234567890abcdef12345678";
const DATE = new Date("2026-09-24T23:59:00Z");

describe("nightlyVersion", () => {
  const version = nightlyVersion({ tags: ["v0.1.0"], version: "0.1.0", date: DATE, sha: SHA });

  it("names the next patch, the UTC date and the short sha", () => {
    assert.equal(version, "0.1.1-0.nightly.20260924.gabc1234");
  });

  it("is valid semver that version.mjs reads as a nightly", () => {
    assert.match(version, SEMVER);
    assert.equal(parse(version).channel, "nightly");
  });

  it("prefixes the sha with a letter, so an all-digit sha is no numeric identifier", () => {
    const digits = nightlyVersion({
      tags: [],
      version: "0.0.0",
      date: DATE,
      sha: "0123456000000000000000000000000000000000",
    });
    assert.match(digits, SEMVER);
    assert.ok(digits.endsWith(".g0123456"));
  });

  it("sorts below any named prerelease of its base", () => {
    const pre = version.split("-")[1];
    for (const named of ["alpha", "alpha.1", "beta.1", "rc.1", "next"]) {
      assert.ok(comparePrerelease(pre, named) < 0, `${pre} sorts below ${named}`);
    }
  });

  it("sorts by date before sha", () => {
    const earlier = "0.nightly.20260923.gfffffff";
    assert.ok(comparePrerelease(earlier, version.split("-")[1]) < 0);
  });

  it("stamps the date in UTC", () => {
    assert.equal(stamp(new Date("2026-01-02T00:30:00+03:00")), "20260101");
  });

  it("refuses a short sha", () => {
    assert.throws(() => nightlyVersion({ tags: [], version: "0.0.0", date: DATE, sha: "abc1234" }));
  });
});

describe("base", () => {
  it("steps the patch of the highest stable tag, numerically", () => {
    assert.equal(base(["v0.9.0", "v0.10.2", "v0.10.10", "v0.11.0-rc.1"], "0.11.0-rc.1"), "0.10.11");
  });

  it("ignores release candidates, nightlies and foreign tags", () => {
    assert.equal(
      base(
        ["v0.2.0", "v0.3.0-rc.1", "v0.2.1-0.nightly.20260923.gabc1234", "sdk/v9.0.0", "vnext"],
        "0.3.0-rc.1",
      ),
      "0.2.1",
    );
  });

  it("steps from VERSION when no stable tag exists yet", () => {
    assert.equal(base([], "0.0.0"), "0.0.1");
  });

  it("takes a release candidate's base when nothing stable exists yet", () => {
    assert.equal(base(["v0.1.0-rc.1"], "0.1.0-rc.1"), "0.1.0");
  });
});

describe("plan", () => {
  it("names the version to tag", () => {
    assert.deepEqual(plan({ tags: [], version: "0.0.0", date: DATE, sha: SHA }), {
      version: "0.0.1-0.nightly.20260924.gabc1234",
    });
  });

  it("skips a commit already released as a nightly", () => {
    const outcome = plan({
      tags: ["v0.0.1-0.nightly.20260920.gabc1234"],
      version: "0.0.0",
      date: DATE,
      sha: SHA,
    });
    assert.match(outcome.skip, /already released as v0\.0\.1-0\.nightly\.20260920\.gabc1234/);
  });

  it("skips a second nightly of the same base and day, which PyPI could not tell apart", () => {
    const outcome = plan({
      tags: ["v0.0.1-0.nightly.20260924.g1111111"],
      version: "0.0.0",
      date: DATE,
      sha: SHA,
    });
    assert.match(outcome.skip, /0\.0\.1\.dev20260924/);
  });

  it("tags a nightly of the same day once a release moved the base", () => {
    const outcome = plan({
      tags: ["v0.0.1-0.nightly.20260924.g1111111", "v0.0.1"],
      version: "0.0.1",
      date: DATE,
      sha: SHA,
    });
    assert.equal(outcome.version, "0.0.2-0.nightly.20260924.gabc1234");
  });
});

describe("stale", () => {
  const tags = [
    "v0.0.1",
    "v0.0.2-rc.1",
    "v0.0.2-0.nightly.20260824.gaaaaaaa",
    "v0.0.2-0.nightly.20260825.gbbbbbbb",
    "v0.0.2-0.nightly.20260924.gccccccc",
  ];

  it("names only the nightlies older than the window", () => {
    assert.deepEqual(stale(tags, new Date("2026-09-24T12:00:00Z"), 30), [
      "v0.0.2-0.nightly.20260824.gaaaaaaa",
    ]);
  });
});
