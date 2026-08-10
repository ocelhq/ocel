import { expect, test } from "vitest";
import { TooLargeError, readCapped } from "../src/stream.mjs";

async function* chunks(...values: Uint8Array[]): AsyncIterable<Uint8Array> {
  for (const value of values) yield value;
}

test("joins the chunks in order", async () => {
  const result = await readCapped(
    chunks(new Uint8Array([1, 2]), new Uint8Array([3]), new Uint8Array([4, 5])),
    16,
  );
  expect([...result]).toEqual([1, 2, 3, 4, 5]);
});

test("a body exactly at the limit is accepted", async () => {
  const result = await readCapped(chunks(new Uint8Array(8)), 8);
  expect(result.byteLength).toBe(8);
});

test("one byte over is refused", async () => {
  await expect(readCapped(chunks(new Uint8Array(9)), 8)).rejects.toThrow(TooLargeError);
});

test("an unbounded source is stopped rather than buffered", async () => {
  let produced = 0;
  async function* forever(): AsyncIterable<Uint8Array> {
    for (;;) {
      produced += 1;
      yield new Uint8Array(1024);
    }
  }
  await expect(readCapped(forever(), 4096)).rejects.toThrow(TooLargeError);
  expect(produced).toBe(5);
});
