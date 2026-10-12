import assert from "node:assert/strict";
import type { Check } from "./context";

const MIGRATED = /migrated: (\d+) notes/;

export const migratedBeforeTheBuildCheck: Check = {
  title:
    "GET /api/notes answers the rows lifecycle.preBuild migrated into the deployed postgres before the app was built",
  run: async (ctx) => {
    const res = await ctx.fetch(new URL("/api/notes", ctx.baseUrl));
    assert.equal(
      res.status,
      200,
      `/api/notes answered ${res.status}, so the notes table lifecycle.preBuild creates is missing`,
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

const NOTE = /<li>prerendered note (?:<!-- -->)?([^<]+)<\/li>/g;
const PRERENDERED_AT = /<p id="at">prerendered at (?:<!-- -->)?([^<]+)<\/p>/;

export const prerenderedNotes = (page: string): string[] =>
  [...page.matchAll(NOTE)].map((found) => found[1]);

export const prerenderedFromTheMigrationCheck: Check = {
  title:
    "GET /notes answers the page next build prerendered from the rows lifecycle.preBuild migrated into the deployed postgres",
  run: async (ctx) => {
    const read = async () => {
      const res = await ctx.fetch(new URL("/notes", ctx.baseUrl));
      assert.equal(res.status, 200, `/notes answered ${res.status}`);
      return res.text();
    };
    const first = await read();
    assert.deepEqual(
      prerenderedNotes(first),
      ["first", "second"],
      `the prerendered page lists other notes than lifecycle.preBuild migrated:\n${first}`,
    );
    const at = PRERENDERED_AT.exec(first)?.[1];
    assert.ok(at, `/notes names no time it was prerendered at:\n${first}`);
    assert.equal(
      PRERENDERED_AT.exec(await read())?.[1],
      at,
      "/notes was rendered again for a second request, so it read the notes at request time rather than at build",
    );
  },
};

export const preBuildPrerenderChecks: Check[] = [prerenderedFromTheMigrationCheck];
