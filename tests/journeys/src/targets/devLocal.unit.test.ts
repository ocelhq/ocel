import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { isStranded, projectSlug } from "../identity";
import { databaseName, journeySlugs } from "./devLocal";

describe("a cell's database", () => {
  it("is named by the slug itself, so two cells never share one", () => {
    const slugs = [
      projectSlug("sdk/next", "42"),
      "j-local-my_user-sdk-next",
      "j-local-my-user-sdk-next",
    ];

    const names = slugs.map(databaseName);

    assert.equal(new Set(names).size, slugs.length);
    assert.deepEqual(journeySlugs(names), slugs);
  });

  it("refuses a slug a quoted identifier cannot carry", () => {
    assert.throws(() => databaseName(`j-1-a"; DROP DATABASE "postgres`));
    assert.throws(() => databaseName(`j-1-${"a".repeat(63)}`));
  });

  it("sweeps the journey databases another run left behind, and nothing else", () => {
    const mine = projectSlug("sdk/next", "42");
    const theirs = projectSlug("sdk/next", "41");
    const names = [
      "postgres",
      "template1",
      "j_lookalike",
      databaseName(mine),
      databaseName(theirs),
    ];

    const stranded = journeySlugs(names).filter((slug) => isStranded(slug, "42"));

    assert.deepEqual(stranded, [theirs]);
  });
});
