import { describe, expect, it } from "vitest";
import { assetPath, firstChunkWith, form, marker, markerOrNone, stamp } from "./html";

describe("marker", () => {
  it("reads the text an ocel marker wraps", () => {
    expect(marker('<span data-ocel="param:slug">a%20b</span>', "param:slug")).toBe("a%20b");
  });

  it("drops the comments react writes between adjacent expressions", () => {
    expect(marker('<p data-ocel="x">one<!-- -->two</p>', "x")).toBe("onetwo");
  });

  it("unescapes the entities react writes for markup characters", () => {
    expect(marker('<p data-ocel="x">a &amp; &lt;b&gt;</p>', "x")).toBe("a & <b>");
  });

  it("names the marker it could not find", () => {
    expect(() => marker("<p>nothing</p>", "gone")).toThrow(/data-ocel="gone"/);
  });
});

describe("markerOrNone", () => {
  it("reads a marker that is there and answers with nothing for one that is not", () => {
    const html = '<span data-ocel="static:cached">at</span>';
    expect(markerOrNone(html, "static:cached")).toBe("at");
    expect(markerOrNone(html, "static:live")).toBeUndefined();
  });
});

describe("stamp", () => {
  it("reads the cached and live halves of one scope", () => {
    const html = `
      <div data-ocel="runtime:cached">nodejs</div>
      <div data-ocel="runtime:live">17</div>
    `;
    expect(stamp(html, "runtime")).toEqual({ cached: "nodejs", live: "17" });
  });
});

describe("form", () => {
  it("reads the action, method and hidden fields", () => {
    const html = `
      <form action="/actions" method="POST" encType="multipart/form-data">
        <input type="hidden" name="$ACTION_ID_abc" value=""/>
        <input name="note" value="ignored"/>
        <button type="submit">go</button>
      </form>
    `;
    expect(form(html)).toEqual({
      action: "/actions",
      method: "post",
      fields: { $ACTION_ID_abc: "" },
    });
  });

  it("refuses a page that rendered no form", () => {
    expect(() => form("<main></main>")).toThrow(/no form/);
  });
});

describe("assetPath", () => {
  it("reads the first hashed asset the page links, whichever quote it wears", () => {
    const html = `
      <link rel="preload" href="/_next/static/css/8fd1.css"/>
      <script src='/_next/static/chunks/0eir.js'></script>
    `;
    expect(assetPath(html)).toBe("/_next/static/css/8fd1.css");
  });

  it("refuses a page that links none", () => {
    expect(() => assetPath("<main></main>")).toThrow(/hashed static asset/);
  });
});

describe("firstChunkWith", () => {
  it("answers the index of the earliest chunk carrying the sentinel", () => {
    expect(firstChunkWith(["a", "ocel-shell", "ocel-deferred"], "ocel-deferred")).toBe(2);
  });

  it("refuses a stream the sentinel never reached", () => {
    expect(() => firstChunkWith(["a"], "ocel-deferred")).toThrow(/ocel-deferred/);
  });
});
