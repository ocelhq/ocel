import { inflateRawSync } from "node:zlib";

import { describe, expect, it } from "vitest";

import { crc32, zipArchive } from "./zip.mjs";

function readArchive(archive) {
  const end = archive.length - 22;
  expect(archive.readUInt32LE(end)).toBe(0x06054b50);
  const count = archive.readUInt16LE(end + 10);
  let at = archive.readUInt32LE(end + 16);
  const entries = [];
  for (let index = 0; index < count; index += 1) {
    expect(archive.readUInt32LE(at)).toBe(0x02014b50);
    const nameLength = archive.readUInt16LE(at + 28);
    const compressedSize = archive.readUInt32LE(at + 20);
    const uncompressedSize = archive.readUInt32LE(at + 24);
    const checksum = archive.readUInt32LE(at + 16);
    const mode = archive.readUInt32LE(at + 38) >>> 16;
    const localAt = archive.readUInt32LE(at + 42);
    const name = archive.toString("utf8", at + 46, at + 46 + nameLength);

    expect(archive.readUInt32LE(localAt)).toBe(0x04034b50);
    const localNameLength = archive.readUInt16LE(localAt + 26);
    const localExtraLength = archive.readUInt16LE(localAt + 28);
    const dataAt = localAt + 30 + localNameLength + localExtraLength;
    const data = inflateRawSync(archive.subarray(dataAt, dataAt + compressedSize));

    entries.push({ name, data, checksum, mode, uncompressedSize });
    at += 46 + nameLength + archive.readUInt16LE(at + 30) + archive.readUInt16LE(at + 32);
  }
  return entries;
}

describe("crc32", () => {
  it("matches the known checksum of the standard vector", () => {
    expect(crc32(Buffer.from("123456789"))).toBe(0xcbf43926);
  });

  it("checksums an empty buffer as zero", () => {
    expect(crc32(Buffer.alloc(0))).toBe(0);
  });
});

describe("zipArchive", () => {
  it("round-trips a lambda bundle", () => {
    const bundle = Buffer.from(`export const handler = async () => ({ statusCode: 200 });\n`.repeat(200));
    const entries = readArchive(zipArchive([{ name: "index.mjs", data: bundle }]));
    expect(entries).toHaveLength(1);
    expect(entries[0].name).toBe("index.mjs");
    expect(entries[0].data.equals(bundle)).toBe(true);
    expect(entries[0].checksum).toBe(crc32(bundle));
    expect(entries[0].uncompressedSize).toBe(bundle.length);
  });

  it("marks entries readable so Lambda can load them", () => {
    const [entry] = readArchive(zipArchive([{ name: "index.mjs", data: Buffer.from("x") }]));
    expect(entry.mode).toBe(0o100644);
  });

  it("actually compresses", () => {
    const bundle = Buffer.alloc(64 * 1024, 0x61);
    expect(zipArchive([{ name: "index.mjs", data: bundle }]).length).toBeLessThan(bundle.length / 10);
  });

  it("carries every entry it is given", () => {
    const entries = readArchive(
      zipArchive([
        { name: "index.mjs", data: Buffer.from("one") },
        { name: "nested/two.mjs", data: Buffer.from("two") },
      ]),
    );
    expect(entries.map((entry) => entry.name)).toEqual(["index.mjs", "nested/two.mjs"]);
    expect(entries.map((entry) => entry.data.toString())).toEqual(["one", "two"]);
  });

  it("is byte-for-byte reproducible", () => {
    const make = () => zipArchive([{ name: "index.mjs", data: Buffer.from("stable") }]);
    expect(make().equals(make())).toBe(true);
  });
});
