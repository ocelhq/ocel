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
  it("empties the body of a marked response, and drops the mark", () => {
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

describe("the remapped-header restorer", () => {
  it("hands the client the header a Function URL remapped, under its own name, and no remapped name", () => {
    const answered = load()(
      response({
        "x-amzn-remapped-www-authenticate": 'Bearer realm="ocel"',
        "x-amzn-remapped-date": "Mon, 29 Sep 2026 00:00:00 GMT",
        date: "Mon, 29 Sep 2026 00:00:01 GMT",
      }),
    );

    expect(answered.headers["www-authenticate"].value).toBe('Bearer realm="ocel"');
    expect(answered.headers.date.value).toBe("Mon, 29 Sep 2026 00:00:01 GMT");
    expect(
      Object.keys(answered.headers).filter((name) => name.startsWith("x-amzn-remapped-")),
    ).toEqual([]);
  });

  it("never restores a header CloudFront refuses a function to add", () => {
    const answered = load()(response({ "x-amzn-remapped-connection": "close" }));

    expect(answered.headers.connection).toBeUndefined();
    expect(answered.headers["x-amzn-remapped-connection"]).toBeUndefined();
  });
});
