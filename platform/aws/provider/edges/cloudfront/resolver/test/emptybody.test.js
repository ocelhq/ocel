import { readFileSync } from "node:fs";
import { EMPTY_BODY_HEADER } from "@platform/edge-contract/empty-body";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("../src/emptybody.js", import.meta.url), "utf8");

function load() {
  return new Function(`${source}\nreturn handler;`)();
}

function response(headers = {}, body) {
  const wire = {};
  for (const [name, value] of Object.entries(headers)) wire[name] = { value };
  const answered = { statusCode: 200, statusDescription: "OK", headers: wire };
  if (body !== undefined) answered.body = body;
  return { response: answered };
}

describe("the empty-body dropper", () => {
  it("empties the body a marked response carries, and drops the mark", () => {
    const answered = load()(response({ [EMPTY_BODY_HEADER]: "1", "content-type": "text/html" }));

    expect(answered.body).toEqual({ encoding: "text", data: "" });
    expect(answered.headers[EMPTY_BODY_HEADER]).toBeUndefined();
    expect(answered.headers["content-type"].value).toBe("text/html");
    expect(answered.statusCode).toBe(200);
  });

  it("leaves an unmarked response exactly as the origin sent it", () => {
    const sent = response({ "content-type": "text/html" }, { encoding: "text", data: "hello" });

    const answered = load()(sent);

    expect(answered).toBe(sent.response);
    expect(answered.body).toEqual({ encoding: "text", data: "hello" });
  });
});
