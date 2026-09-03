import { describe, expect, it } from "vitest";

import { parseDotenv } from "./dotenv";

const parse = (text: string) =>
  Object.fromEntries(parseDotenv(text).map((entry) => [entry.key, entry.value]));

describe("parseDotenv", () => {
  it("reads plain assignments and export prefixes", () => {
    expect(parse("A=1\nexport B=two\nC = spaced ")).toEqual({
      A: "1",
      B: "two",
      C: "spaced",
    });
  });

  it("keeps equals signs inside values", () => {
    expect(parse("URL=postgres://u:p@h/db?sslmode=require&x=1")).toEqual({
      URL: "postgres://u:p@h/db?sslmode=require&x=1",
    });
  });

  it("skips blank lines and full-line comments, and accepts CRLF", () => {
    expect(parse("\r\n# note\r\nA=1\r\n\r\n  # indented\r\nB=2\r\n")).toEqual({
      A: "1",
      B: "2",
    });
  });

  it("strips a trailing comment after an unquoted value only when whitespace precedes the hash", () => {
    expect(parse("A=one # comment\nB=#ff0000\nC=x#y\nD= # nothing")).toEqual({
      A: "one",
      B: "#ff0000",
      C: "x#y",
      D: "",
    });
  });

  it("keeps a hash inside quotes", () => {
    expect(parse(`A="one # not a comment"\nB='two # also not'`)).toEqual({
      A: "one # not a comment",
      B: "two # also not",
    });
  });

  it("unescapes inside double quotes but not single quotes", () => {
    expect(parse(String.raw`A="line\nnext\t\"q\" \\"` + "\n" + String.raw`B='raw\nstays'`)).toEqual({
      A: 'line\nnext\t"q" \\',
      B: String.raw`raw\nstays`,
    });
  });

  it("trims nothing inside quotes and ignores what follows the closing quote", () => {
    expect(parse(`A="  padded  " # trailing\nB='x'y`)).toEqual({
      A: "  padded  ",
      B: "x",
    });
  });

  it("joins a quoted value that spans lines", () => {
    expect(parse(`PEM="-----BEGIN-----\nabc\n-----END-----"\nNEXT=1`)).toEqual({
      PEM: "-----BEGIN-----\nabc\n-----END-----",
      NEXT: "1",
    });
  });

  it("takes what it has when a quote never closes", () => {
    expect(parse(`A="open\nB=1`)).toEqual({ A: "open\nB=1" });
  });

  it("lets a later assignment win and ignores lines that are not assignments", () => {
    expect(parse("A=1\nnot an assignment\n1BAD=x\nA=2")).toEqual({ A: "2" });
    expect(parseDotenv("A=1\nA=2").map((entry) => entry.key)).toEqual(["A"]);
  });
});
