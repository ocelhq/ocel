import type { DescField, DescMessage } from "@bufbuild/protobuf";
import { getOption, hasOption } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { file_app_bucket_v1_bucket } from "./gen/proto/app/bucket/v1/bucket_pb.js";
import { file_app_resources_v1_variables } from "./gen/proto/app/resources/v1/variables_pb.js";
import { field } from "./gen/proto/buf/validate/validate_pb.js";

const files = [file_app_bucket_v1_bucket, file_app_resources_v1_variables];

const patternsOf = (message: DescMessage): { field: string; pattern: string }[] =>
  message.fields.flatMap((f: DescField) => {
    if (!hasOption(f, field)) return [];
    const rules = getOption(f, field);
    const found: { field: string; pattern: string }[] = [];
    if (rules.type.case === "string" && rules.type.value.pattern !== "") {
      found.push({ field: f.toString(), pattern: rules.type.value.pattern });
    }
    if (rules.type.case === "repeated") {
      const items = rules.type.value.items?.type;
      if (items?.case === "string" && items.value.pattern !== "") {
        found.push({ field: f.toString(), pattern: items.value.pattern });
      }
    }
    return found;
  });

const shipped = files.flatMap((file) => file.messages.flatMap(patternsOf));

describe("buf.validate string patterns the generated bindings carry", () => {
  it("are found in the descriptors", () => {
    expect(shipped.length).toBeGreaterThan(0);
  });

  it("compile as ECMA-262 regular expressions with the u flag", () => {
    for (const { field: name, pattern } of shipped) {
      expect(() => new RegExp(pattern, "u"), `${name}: ${pattern}`).not.toThrow();
    }
  });

  it("refuse a control character and a hash and admit ordinary text", () => {
    const name = shipped.find((p) => p.pattern === "^[^#\\x00-\\x1f\\x7f]*$");
    expect(name).toBeDefined();
    const re = new RegExp(name?.pattern ?? "", "u");
    for (const text of ["", "a", "a b", "a/b", "é", "a@b"]) expect(re.test(text), text).toBe(true);
    for (const text of [
      "#",
      "a#b",
      "a\x00b",
      "a\x1fb",
      "a\x7fb",
      "a\tb",
      "a\nb",
      "a\vb",
      "a\fb",
      "a\rb",
    ]) {
      expect(re.test(text), JSON.stringify(text)).toBe(false);
    }

    const folder = shipped.find((p) => p.pattern === "^(/[^/#\\x00-\\x1f\\x7f]+)+$");
    expect(folder).toBeDefined();
    const folderRe = new RegExp(folder?.pattern ?? "", "u");
    for (const text of ["/a", "/a/b", "/a b"]) expect(folderRe.test(text), text).toBe(true);
    for (const text of ["", "/", "a", "//a", "/a#b", "/a\x00", "/a\x7f", "/a\n"]) {
      expect(folderRe.test(text), JSON.stringify(text)).toBe(false);
    }
  });
});
