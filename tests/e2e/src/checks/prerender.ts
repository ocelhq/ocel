import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import type { Check } from "./context";

const run = promisify(execFile);

const IMAGE_REF = path.join(".ocel", "output", "apps", "web", "image-ref");
const SHOWN_BINDINGS = [
  /OCEL_RESOURCE_[A-Z]+_[\w-]+=/g,
  /postgres(?:ql)?:\/\/[^\s/@:"]+:[^\s/@"]+@/g,
  /\\?"password\\?"\s*:/g,
];

export function findBindingsShown(text: string): string[] {
  return SHOWN_BINDINGS.flatMap((pattern) => [...text.matchAll(pattern)].map((found) => found[0]));
}

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

export const imageShowsNoBindingCheck: Check = {
  title: "the image next build ran in shows no binding in its history, config or labels",
  run: async (ctx) => {
    const ref = (await readFile(path.join(ctx.projectDir, IMAGE_REF), "utf8")).trim();
    for (const args of [
      ["image", "history", "--no-trunc", "--format", "{{.CreatedBy}}", ref],
      ["image", "inspect", ref],
    ]) {
      const { stdout } = await run("docker", args, { maxBuffer: 16 * 1024 * 1024 });
      assert.deepEqual(
        findBindingsShown(stdout),
        [],
        `docker ${args.slice(0, 2).join(" ")} of ${ref} shows a binding the build read`,
      );
    }
  },
};

const BUCKET = /prerendered from a bucket holding (?:<!-- -->)?(\d+)(?:<!-- -->)? objects/;
const MISSING = /and (?:<!-- -->)?no(?:<!-- -->)? object at a key nothing wrote/;

export const prerenderedFromTheBucketCheck: Check = {
  title: "GET / answers the page next build prerendered from the deployed bucket, once",
  run: async (ctx) => {
    const first = await page(ctx, "/");
    assert.equal(first.res.status, 200);
    assert.ok(
      BUCKET.test(first.body),
      `the home page names no bucket it was prerendered from, so the build never reached it:\n${first.body}`,
    );
    assert.ok(
      MISSING.test(first.body),
      `the home page found an object at a key nothing wrote:\n${first.body}`,
    );
    const at = AT.exec(first.body)?.[1];
    assert.ok(at, `the home page names no time it was prerendered at:\n${first.body}`);

    const second = await page(ctx, "/");
    assert.equal(
      AT.exec(second.body)?.[1],
      at,
      "the home page was rendered again for a second request, so it read the bucket at request time rather than at build",
    );
  },
};

export const prerenderBucketChecks: Check[] = [prerenderedFromTheBucketCheck];

export const prerenderChecks: Check[] = [
  prerenderedFromTheDatabaseCheck,
  staticParamsFromTheDatabaseCheck,
  imageShowsNoBindingCheck,
];
