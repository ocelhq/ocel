import { describe, expect, it } from "vitest";
import { steady } from "./nextApp";

function reading(values: string[]): () => Promise<string> {
  let at = 0;
  return async () => values[Math.min(at++, values.length - 1)]!;
}

describe("steady", () => {
  it("answers with the value every read of an attempt agreed on", async () => {
    await expect(steady(reading(["a", "a", "a"]), "/cache/isr", 3, 3)).resolves.toBe("a");
  });

  it("starts over when a window turned over mid-attempt and settles on the next one", async () => {
    await expect(steady(reading(["a", "a", "b", "b", "b", "b"]), "/cache/isr", 3, 3)).resolves.toBe(
      "b",
    );
  });

  it("fails when every attempt straddled a turn", async () => {
    const alternating = () => {
      let at = 0;
      return async () => String(at++ % 2);
    };
    await expect(steady(alternating(), "/cache/isr", 3, 3)).rejects.toThrow(/3 attempts/);
  });
});
