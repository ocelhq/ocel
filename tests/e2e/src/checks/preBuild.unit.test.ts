import { describe, expect, it } from "bun:test";
import { prerenderedNotes } from "./preBuild";

describe("prerenderedNotes", () => {
  it("lists the notes the page renders and none its flight payload repeats", () => {
    const page = [
      '<p id="at">prerendered at <!-- -->2026-10-08T21:58:01.000Z</p>',
      "<ul><li>prerendered note <!-- -->first</li><li>prerendered note <!-- -->second</li></ul>",
      '<script>self.__next_f.push([1,"[\\"$\\",\\"li\\",\\"first\\",{\\"children\\":[\\"prerendered note \\",\\"first\\"]}],[\\"$\\",\\"li\\",\\"second\\",{\\"children\\":[\\"prerendered note \\",\\"second\\"]}]"])</script>',
    ].join("");

    expect(prerenderedNotes(page)).toEqual(["first", "second"]);
  });
});
