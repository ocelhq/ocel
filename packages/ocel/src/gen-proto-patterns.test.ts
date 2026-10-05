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
  it("compile as ECMA-262 regular expressions with the u flag", () => {
    expect(shipped.length).toBeGreaterThan(0);
    for (const { field: name, pattern } of shipped) {
      expect(() => new RegExp(pattern, "u"), `${name}: ${pattern}`).not.toThrow();
    }
  });
});
