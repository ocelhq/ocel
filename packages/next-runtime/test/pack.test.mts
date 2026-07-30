import { afterEach, expect, test, vi } from "vitest";
import { defaultBudgetBytes, packBundles } from "../src/pack.mts";

afterEach(() => {
  vi.restoreAllMocks();
});

interface Route {
  key: string;
  assets: Record<string, string>;
  group?: string;
}

const mb = (n: number) => n * 1024 * 1024;

// Every test injects sizes, so nothing here reads the filesystem: a path's size
// is whatever the fake table says, and an unlisted path is a 1-byte file.
function pack(routes: readonly Route[], sizes: Record<string, number> = {}, opts: {
  budgetBytes?: number;
  partitionBy?: (r: Route) => string;
} = {}) {
  return packBundles(routes, {
    entryKeyOf: (r) => r.key,
    assetsOf: (r) => r.assets,
    sizeOf: (abs) => sizes[abs] ?? 1,
    ...opts,
  });
}

test("no members yields no bundles", () => {
  expect(pack([])).toEqual([]);
});

test("a normal app packs into exactly one bundle", () => {
  const shared = { "node_modules/next/index.js": "/abs/next" };
  const bundles = pack([
    { key: "a", assets: { ...shared, "app/a.js": "/abs/a" } },
    { key: "b", assets: { ...shared, "app/b.js": "/abs/b" } },
    { key: "c", assets: { ...shared, "app/c.js": "/abs/c" } },
  ]);

  expect(bundles).toHaveLength(1);
  expect(bundles[0]!.name).toBe("bundle-0");
  expect(bundles[0]!.members.map((m) => m.key)).toEqual(["a", "b", "c"]);
});

test("a bundle carries the union of its members' assets", () => {
  const bundles = pack([
    { key: "a", assets: { "shared.js": "/abs/shared", "a.js": "/abs/a" } },
    { key: "b", assets: { "shared.js": "/abs/shared", "b.js": "/abs/b" } },
  ]);

  expect(bundles[0]!.assets).toEqual({
    "shared.js": "/abs/shared",
    "a.js": "/abs/a",
    "b.js": "/abs/b",
  });
});

test("budget is measured over the union, not the sum per member", () => {
  const sizes = { "/abs/forest": mb(150), "/abs/a": mb(1), "/abs/b": mb(1) };
  const forest = { "node_modules/forest": "/abs/forest" };

  const bundles = pack(
    [
      { key: "a", assets: { ...forest, "a.js": "/abs/a" } },
      { key: "b", assets: { ...forest, "b.js": "/abs/b" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles).toHaveLength(1);
  expect(bundles[0]!.sizeBytes).toBe(mb(152));
});

test("a member whose delta blows the budget starts a new bundle", () => {
  const sizes = { "/abs/a": mb(120), "/abs/b": mb(120), "/abs/c": mb(10) };

  const bundles = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
      { key: "c", assets: { "c.js": "/abs/c" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map((b) => [b.name, b.members.map((m) => m.key)])).toEqual([
    ["bundle-0", ["a"]],
    ["bundle-1", ["b", "c"]],
  ]);
});

test("members are packed in entry-key order regardless of input order", () => {
  const routes: Route[] = [
    { key: "c", assets: { "c.js": "/abs/c" } },
    { key: "a", assets: { "a.js": "/abs/a" } },
    { key: "b", assets: { "b.js": "/abs/b" } },
  ];

  const forward = pack(routes);
  const reversed = pack([...routes].reverse());

  expect(forward[0]!.members.map((m) => m.key)).toEqual(["a", "b", "c"]);
  expect(reversed).toEqual(forward);
});

test("partitions never share a bundle and are named in sorted key order", () => {
  const bundles = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" }, group: "z" },
      { key: "b", assets: { "b.js": "/abs/b" }, group: "m" },
      { key: "c", assets: { "c.js": "/abs/c" }, group: "z" },
    ],
    {},
    { partitionBy: (r) => r.group! },
  );

  expect(bundles.map((b) => [b.name, b.members.map((m) => m.key)])).toEqual([
    ["bundle-0", ["b"]],
    ["bundle-1", ["a", "c"]],
  ]);
});

test("a partition splits on budget independently of the others", () => {
  const sizes = { "/abs/a": mb(150), "/abs/b": mb(150), "/abs/c": mb(1) };

  const bundles = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" }, group: "x" },
      { key: "b", assets: { "b.js": "/abs/b" }, group: "x" },
      { key: "c", assets: { "c.js": "/abs/c" }, group: "y" },
    ],
    sizes,
    { budgetBytes: mb(200), partitionBy: (r) => r.group! },
  );

  expect(bundles.map((b) => [b.name, b.members.map((m) => m.key)])).toEqual([
    ["bundle-0", ["a"]],
    ["bundle-1", ["b"]],
    ["bundle-2", ["c"]],
  ]);
});

test("one dest-key mapped to two source paths throws naming both", () => {
  expect(() =>
    pack([
      { key: "a", assets: { "shared.js": "/abs/one" } },
      { key: "b", assets: { "shared.js": "/abs/two" } },
    ]),
  ).toThrow(/shared\.js[\s\S]*\/abs\/one[\s\S]*\/abs\/two/);
});

test("a conflict across partitions is still a conflict", () => {
  expect(() =>
    pack(
      [
        { key: "a", assets: { "shared.js": "/abs/one" }, group: "x" },
        { key: "b", assets: { "shared.js": "/abs/two" }, group: "y" },
      ],
      {},
      { partitionBy: (r) => r.group! },
    ),
  ).toThrow(/shared\.js/);
});

test("the same dest-key at the same path is the expected sharing", () => {
  expect(() =>
    pack([
      { key: "a", assets: { "shared.js": "/abs/one" } },
      { key: "b", assets: { "shared.js": "/abs/one" } },
    ]),
  ).not.toThrow();
});

test("a member larger than the budget gets its own bundle and a warning", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const sizes = { "/abs/huge": mb(300), "/abs/small": mb(1) };

  const bundles = pack(
    [
      { key: "huge", assets: { "huge.js": "/abs/huge" } },
      { key: "small", assets: { "small.js": "/abs/small" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map((b) => b.members.map((m) => m.key))).toEqual([
    ["huge"],
    ["small"],
  ]);
  expect(warn).toHaveBeenCalledTimes(1);
  expect(warn.mock.calls[0]![0]).toMatch(/huge/);
  expect(warn.mock.calls[0]![0]).toMatch(String(mb(300)));
});

test("consecutive oversized members each get a bundle", () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const sizes = { "/abs/a": mb(300), "/abs/b": mb(400) };

  const bundles = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map((b) => b.members.map((m) => m.key))).toEqual([
    ["a"],
    ["b"],
  ]);
});

test("a member under budget draws no warning", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  pack([{ key: "a", assets: { "a.js": "/abs/a" } }], {}, { budgetBytes: 100 });
  expect(warn).not.toHaveBeenCalled();
});

test("each source path is sized once however many members share it", () => {
  const sizeOf = vi.fn(() => 1);
  packBundles(
    [
      { key: "a", assets: { "shared.js": "/abs/shared" } },
      { key: "b", assets: { "shared.js": "/abs/shared" } },
      { key: "c", assets: { "shared.js": "/abs/shared" } },
    ] satisfies Route[],
    { entryKeyOf: (r) => r.key, assetsOf: (r) => r.assets, sizeOf },
  );

  expect(sizeOf).toHaveBeenCalledTimes(1);
});

test("the default budget is Lambda's unzipped ceiling", () => {
  expect(defaultBudgetBytes).toBe(200 * 1024 * 1024);
});

test("the default budget applies when none is given", () => {
  const sizes = { "/abs/a": defaultBudgetBytes, "/abs/b": 1 };

  const bundles = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
    ],
    sizes,
  );

  expect(bundles).toHaveLength(2);
});
