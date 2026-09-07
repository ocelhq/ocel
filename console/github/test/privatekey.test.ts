import { createPrivateKey, generateKeyPairSync } from "node:crypto";
import { expect, test } from "vitest";
import { privateKey } from "../src/http";

const pem = generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey.export({
  type: "pkcs1",
  format: "pem",
}) as string;

function parses(value: string): boolean {
  try {
    createPrivateKey(privateKey(value));
    return true;
  } catch {
    return false;
  }
}

test("a pem with real newlines is handed over intact", () => {
  expect(privateKey(pem)).toBe(pem);
  expect(parses(pem)).toBe(true);
});

test("a pem whose newlines became literal backslash-n is restored", () => {
  expect(parses(pem.replace(/\n/g, "\\n"))).toBe(true);
});

test("a pem whose newlines became spaces is restored", () => {
  expect(parses(pem.replace(/\n/g, " "))).toBe(true);
});

test("a pem flattened to one line with no separators is restored", () => {
  const flat = pem.replace(/\n/g, "");
  expect(parses(flat)).toBe(true);
});

test("a value that is not a pem is passed through unchanged", () => {
  expect(privateKey("not a key")).toBe("not a key");
});
