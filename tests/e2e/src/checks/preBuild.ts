import assert from "node:assert/strict";
import type { Refusal } from "../matrix/types";
import type { Check } from "./context";

const MIGRATED = /migrated: (\d+) notes/;

export const migratedBeforeTheBuildCheck: Check = {
  title:
    "GET /notes answers the rows lifecycle.preBuild migrated into the deployed postgres before the app was built",
  run: async (ctx) => {
    const res = await ctx.fetch(new URL("/notes", ctx.baseUrl));
    assert.equal(
      res.status,
      200,
      `/notes answered ${res.status}, so the notes table lifecycle.preBuild creates is missing`,
    );
    assert.deepEqual(await res.json(), { titles: ["first", "second"] });
  },
};

export const runsAgainstTheDeployedEnvironmentCheck: Check = {
  title:
    "ocel run --env production -- pnpm migrate reaches the deployed postgres and finds it already migrated",
  run: async (ctx) => {
    const said = await ctx.runInEnvironment(["pnpm", "migrate"]);
    const migrated = MIGRATED.exec(said)?.[1];
    assert.equal(migrated, "2", `the command did not report the migrated rows:\n${said}`);
  },
};

export const preBuildChecks: Check[] = [
  migratedBeforeTheBuildCheck,
  runsAgainstTheDeployedEnvironmentCheck,
];

const NOTE = /prerendered note (?:<!-- -->)?([^<]+)</g;
const PRERENDERED_AT = /prerendered at (?:<!-- -->)?([^<]+)</;

export const prerenderedFromTheMigrationCheck: Check = {
  title:
    "GET / answers the page next build prerendered from the rows lifecycle.preBuild migrated into the deployed postgres",
  run: async (ctx) => {
    const read = async () => {
      const res = await ctx.fetch(new URL("/", ctx.baseUrl));
      assert.equal(res.status, 200, `/ answered ${res.status}`);
      return res.text();
    };
    const first = await read();
    assert.deepEqual(
      [...first.matchAll(NOTE)].map((found) => found[1]),
      ["first", "second"],
      `the prerendered page lists no notes lifecycle.preBuild migrated:\n${first}`,
    );
    const at = PRERENDERED_AT.exec(first)?.[1];
    assert.ok(at, `the home page names no time it was prerendered at:\n${first}`);
    assert.equal(
      PRERENDERED_AT.exec(await read())?.[1],
      at,
      "the home page was rendered again for a second request, so it read the notes at request time rather than at build",
    );
  },
};

export const preBuildPrerenderChecks: Check[] = [prerenderedFromTheMigrationCheck];

export const failingPreBuildRefusal: Refusal = {
  title:
    "a lifecycle.preBuild that exits non-zero stops the deploy before the app is built, naming its exit code and output",
  run: async (said) => {
    assert.match(said, /exited with code 3/, `the deploy failed for another reason:\n${said}`);
    assert.match(
      said,
      /migration refused: the schema is behind/,
      `the output is missing:\n${said}`,
    );
    assert.doesNotMatch(
      said,
      /Buil(?:t|ding) app web/,
      `the app was built after lifecycle.preBuild failed:\n${said}`,
    );
  },
};
