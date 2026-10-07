import assert from "node:assert/strict";
import type { Check } from "./context";

const DATABASE = /prerendered from database (?:<!-- -->)?([^<\s]+)/;
const AT = /prerendered at (?:<!-- -->)?([^<]+)</;

async function page(ctx: Parameters<Check["run"]>[0], path: string) {
  const res = await ctx.fetch(new URL(path, ctx.baseUrl));
  return { res, body: await res.text() };
}

export const prerenderedFromTheDatabaseCheck: Check = {
  title: "GET / answers the page next build prerendered from the deployed postgres, once",
  run: async (ctx) => {
    const first = await page(ctx, "/");
    assert.equal(first.res.status, 200);
    const database = DATABASE.exec(first.body)?.[1];
    assert.ok(database, `the home page names no database it was prerendered from:\n${first.body}`);
    const at = AT.exec(first.body)?.[1];
    assert.ok(at, `the home page names no time it was prerendered at:\n${first.body}`);

    const second = await page(ctx, "/");
    assert.equal(
      AT.exec(second.body)?.[1],
      at,
      "the home page was rendered again for a second request, so it read postgres at request time rather than at build",
    );
  },
};

export const staticParamsFromTheDatabaseCheck: Check = {
  title:
    "GET /rows/:n answers each row generateStaticParams read from postgres, and none past them",
  run: async (ctx) => {
    for (const n of [1, 2, 3]) {
      const { res, body } = await page(ctx, `/rows/${n}`);
      assert.equal(res.status, 200, `/rows/${n} answered ${res.status}`);
      assert.match(
        body,
        new RegExp(`row (?:<!-- -->)?${n}(?:<!-- -->)? squared is (?:<!-- -->)?${n * n}`),
      );
    }
    const past = await page(ctx, "/rows/4");
    assert.equal(
      past.res.status,
      404,
      "/rows/4 answered, so the route serves params generateStaticParams never read",
    );
  },
};

export const prerenderChecks: Check[] = [
  prerenderedFromTheDatabaseCheck,
  staticParamsFromTheDatabaseCheck,
];
