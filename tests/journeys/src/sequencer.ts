import path from "node:path";
import { BaseSequencer, type TestSpecification } from "vitest/node";
import { exampleOf, longestFirst } from "./order";
import { spec } from "./spec";

const ORDER = new Map(longestFirst(spec).map((row, index) => [row.name, index]));

function rankOf(file: TestSpecification): number {
  return ORDER.get(exampleOf(path.basename(file.moduleId))) ?? ORDER.size;
}

export default class LongestFirstSequencer extends BaseSequencer {
  async sort(files: TestSpecification[]): Promise<TestSpecification[]> {
    return [...files].sort((a, b) => rankOf(a) - rankOf(b));
  }
}
