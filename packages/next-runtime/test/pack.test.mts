import { lstat, mkdir, mkdtemp, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
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

const keys = (bundle: { members: { member: Route }[] }) =>
  bundle.members.map((m) => m.member.key);

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
  expect(pack([]).bundles).toEqual([]);
});

test("a normal app packs into exactly one bundle", () => {
  const shared = { "node_modules/next/index.js": "/abs/next" };
  const { bundles } = pack([
    { key: "a", assets: { ...shared, "app/a.js": "/abs/a" } },
    { key: "b", assets: { ...shared, "app/b.js": "/abs/b" } },
    { key: "c", assets: { ...shared, "app/c.js": "/abs/c" } },
  ]);

  expect(bundles).toHaveLength(1);
  expect(bundles[0]!.name).toBe("bundle-0");
  expect(keys(bundles[0]!)).toEqual(["a", "b", "c"]);
});

test("a bundle carries the union of its members' assets", () => {
  const { bundles } = pack([
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

  const { bundles } = pack(
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

  const { bundles } = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
      { key: "c", assets: { "c.js": "/abs/c" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map((b) => [b.name, keys(b)])).toEqual([
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

  const forward = pack(routes).bundles;
  const reversed = pack([...routes].reverse()).bundles;

  expect(keys(forward[0]!)).toEqual(["a", "b", "c"]);
  expect(reversed).toEqual(forward);
});

test("partitions never share a bundle and are named in sorted key order", () => {
  const { bundles } = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" }, group: "z" },
      { key: "b", assets: { "b.js": "/abs/b" }, group: "m" },
      { key: "c", assets: { "c.js": "/abs/c" }, group: "z" },
    ],
    {},
    { partitionBy: (r) => r.group! },
  );

  expect(bundles.map((b) => [b.name, keys(b)])).toEqual([
    ["bundle-0", ["b"]],
    ["bundle-1", ["a", "c"]],
  ]);
});

test("a partition splits on budget independently of the others", () => {
  const sizes = { "/abs/a": mb(150), "/abs/b": mb(150), "/abs/c": mb(1) };

  const { bundles } = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" }, group: "x" },
      { key: "b", assets: { "b.js": "/abs/b" }, group: "x" },
      { key: "c", assets: { "c.js": "/abs/c" }, group: "y" },
    ],
    sizes,
    { budgetBytes: mb(200), partitionBy: (r) => r.group! },
  );

  expect(bundles.map((b) => [b.name, keys(b)])).toEqual([
    ["bundle-0", ["a"]],
    ["bundle-1", ["b"]],
    ["bundle-2", ["c"]],
  ]);
});

test("members disagreeing on one dest-key never share a bundle", () => {
  const { bundles } = pack([
    { key: "a", assets: { "shared.js": "/abs/one" } },
    { key: "b", assets: { "shared.js": "/abs/two" } },
  ]);

  expect(bundles.map((b) => [keys(b), b.assets])).toEqual([
    [["a"], { "shared.js": "/abs/one" }],
    [["b"], { "shared.js": "/abs/two" }],
  ]);
});

test("a conflict splits once and later agreeing members keep packing", () => {
  const { bundles } = pack([
    { key: "a", assets: { "shared.js": "/abs/one", "a.js": "/abs/a" } },
    { key: "b", assets: { "shared.js": "/abs/two" } },
    { key: "c", assets: { "shared.js": "/abs/two", "c.js": "/abs/c" } },
  ]);

  expect(bundles.map(keys)).toEqual([
    ["a"],
    ["b", "c"],
  ]);
  expect(bundles[1]!.assets).toEqual({
    "shared.js": "/abs/two",
    "c.js": "/abs/c",
  });
});

test("conflict splitting is deterministic regardless of input order", () => {
  const routes: Route[] = [
    { key: "a", assets: { "shared.js": "/abs/one" } },
    { key: "b", assets: { "shared.js": "/abs/two" } },
    { key: "c", assets: { "shared.js": "/abs/one" } },
  ];

  const forward = pack(routes).bundles;
  const reversed = pack([...routes].reverse()).bundles;

  expect(JSON.stringify(reversed)).toBe(JSON.stringify(forward));
  expect(forward.map((b) => [b.name, keys(b)])).toEqual([
    ["bundle-0", ["a"]],
    ["bundle-1", ["b"]],
    ["bundle-2", ["c"]],
  ]);
});

test("a conflict across partitions costs nothing extra", () => {
  const { bundles } = pack(
    [
      { key: "a", assets: { "shared.js": "/abs/one" }, group: "x" },
      { key: "b", assets: { "shared.js": "/abs/two" }, group: "y" },
    ],
    {},
    { partitionBy: (r) => r.group! },
  );

  expect(bundles).toHaveLength(2);
});

test("the same dest-key at the same path is the expected sharing", () => {
  const { bundles } = pack([
    { key: "a", assets: { "shared.js": "/abs/one" } },
    { key: "b", assets: { "shared.js": "/abs/one" } },
  ]);

  expect(bundles).toHaveLength(1);
  expect(bundles[0]!.sizeBytes).toBe(1);
});

test("two members sharing one entry key throws", () => {
  expect(() =>
    pack([
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "a", assets: { "b.js": "/abs/b" } },
    ]),
  ).toThrow(/entry key "a"/);
});

// The packer sizes the assets, so it is the one that knows which sources are
// missing — but the copy site is the one that reports them, so exactly one line
// per build names them however many bundles they land in.
test("an asset source that does not exist comes back on the result, unwarned", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  const { bundles, missingAssets } = packBundles(
    [
      { key: "a", assets: { "gone.js": "/abs/gone", "a.js": "/abs/a" } },
      { key: "b", assets: { "vanished.js": "/abs/vanished" } },
    ] satisfies Route[],
    {
      entryKeyOf: (r) => r.key,
      assetsOf: (r) => r.assets,
      sizeOf: (abs) => (abs.startsWith("/abs/a") ? 7 : undefined),
    },
  );

  expect(bundles).toHaveLength(1);
  expect(bundles[0]!.sizeBytes).toBe(7);
  expect(bundles[0]!.assets["gone.js"]).toBe("/abs/gone");
  expect(missingAssets).toEqual(["gone.js", "vanished.js"]);
  expect(warn).not.toHaveBeenCalled();
});

test("a dest key missing in several bundles is reported once, sorted", () => {
  const { bundles, missingAssets } = packBundles(
    [
      { key: "a", assets: { "z.js": "/abs/one", "gone.js": "/abs/gone" } },
      { key: "b", assets: { "z.js": "/abs/two", "gone.js": "/abs/gone" } },
    ] satisfies Route[],
    {
      entryKeyOf: (r) => r.key,
      assetsOf: (r) => r.assets,
      sizeOf: () => undefined,
    },
  );

  expect(bundles).toHaveLength(2);
  expect(missingAssets).toEqual(["gone.js", "z.js"]);
});

test("no missing sources yields an empty set", () => {
  const routes = [{ key: "a", assets: { "a.js": "/abs/a" } }];
  expect(pack(routes).missingAssets).toEqual([]);
});

test("a member larger than the budget gets its own bundle and a warning", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const sizes = { "/abs/huge": mb(300), "/abs/small": mb(1) };

  const { bundles } = pack(
    [
      { key: "huge", assets: { "huge.js": "/abs/huge" } },
      { key: "small", assets: { "small.js": "/abs/small" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map(keys)).toEqual([
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

  const { bundles } = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
    ],
    sizes,
    { budgetBytes: mb(200) },
  );

  expect(bundles.map(keys)).toEqual([
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

// Per-member bytes are what elects a bundle's primed entry, so they have to be
// the member's own traced weight — not its delta, which depends on who was
// packed before it, and not the bundle's union.
test("each member carries its own traced bytes", () => {
  const sizes = { "/abs/forest": mb(150), "/abs/a": mb(1), "/abs/b": mb(3) };
  const forest = { "node_modules/forest": "/abs/forest" };

  const { bundles } = pack(
    [
      { key: "a", assets: { ...forest, "a.js": "/abs/a" } },
      { key: "b", assets: { ...forest, "b.js": "/abs/b" } },
    ],
    sizes,
  );

  expect(bundles[0]!.members).toEqual([
    { member: expect.objectContaining({ key: "a" }), sizeBytes: mb(151) },
    { member: expect.objectContaining({ key: "b" }), sizeBytes: mb(153) },
  ]);
  expect(bundles[0]!.sizeBytes).toBe(mb(154));
});

test("a member's missing asset costs it nothing", () => {
  const { bundles } = packBundles(
    [
      { key: "a", assets: { "a.js": "/abs/a", "gone.js": "/abs/gone" } },
    ] satisfies Route[],
    {
      entryKeyOf: (r) => r.key,
      assetsOf: (r) => r.assets,
      sizeOf: (abs) => (abs === "/abs/a" ? 9 : undefined),
    },
  );

  expect(bundles[0]!.members[0]!.sizeBytes).toBe(9);
});

// The one sizing implementation both the budget and the primary election rest
// on, and it has to mirror how the asset copy lands each kind of path: a
// symlink is preserved as a link and costs itself, a directory is copied whole
// and costs its recursive contents, and a path that is not there costs nothing.
test("the default sizer costs a path as the copy lands it", async () => {
  const root = await mkdtemp(join(tmpdir(), "ocel-pack-"));
  await mkdir(join(root, "dir", "nested"), { recursive: true });
  await writeFile(join(root, "file.js"), "x".repeat(100));
  await writeFile(join(root, "dir", "one.js"), "x".repeat(40));
  await writeFile(join(root, "dir", "nested", "two.js"), "x".repeat(65));
  await symlink(join(root, "file.js"), join(root, "link.js"));

  const { bundles, missingAssets } = packBundles(
    [
      { key: "a", assets: { "file.js": join(root, "file.js") } },
      { key: "b", assets: { "dir": join(root, "dir") } },
      { key: "c", assets: { "link.js": join(root, "link.js") } },
      { key: "d", assets: { "gone.js": join(root, "gone.js") } },
    ] satisfies Route[],
    { entryKeyOf: (r) => r.key, assetsOf: (r) => r.assets },
  );

  const bytesByKey = new Map(
    bundles.flatMap((b) => b.members.map((m) => [m.member.key, m.sizeBytes])),
  );
  expect(bytesByKey.get("a")).toBe(100);
  expect(bytesByKey.get("b")).toBe(105);
  // Itself, never its target: the target is copied under its own asset entry.
  expect(bytesByKey.get("c")).toBe((await lstat(join(root, "link.js"))).size);
  expect(bytesByKey.get("c")).not.toBe(100);
  expect(bytesByKey.get("d")).toBe(0);
  expect(missingAssets).toEqual(["gone.js"]);
});

test("the default budget applies when none is given", () => {
  const sizes = { "/abs/a": defaultBudgetBytes, "/abs/b": 1 };

  const { bundles } = pack(
    [
      { key: "a", assets: { "a.js": "/abs/a" } },
      { key: "b", assets: { "b.js": "/abs/b" } },
    ],
    sizes,
  );

  expect(bundles).toHaveLength(2);
});
