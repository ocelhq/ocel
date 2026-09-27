import { readFileSync } from "node:fs";

import { expect, it } from "vitest";

import { targetsOf } from "../src/targets.mjs";

interface WrittenTargets {
  table: string;
  class: string;
  project: string;
  items: { pk: string; sk: string; distributions: string[] }[];
}

const written: WrittenTargets = JSON.parse(
  readFileSync(
    new URL("../../../provider/ports/testdata/invalidation-targets.json", import.meta.url),
    "utf8",
  ),
);

class TableAsTheLedgerWritesIt {
  async send(command: any): Promise<any> {
    const { TableName, Key } = command.input;
    const item = written.items.find((each) => each.pk === Key.pk.S && each.sk === Key.sk.S);
    if (TableName !== written.table || item === undefined) return {};
    return {
      Item: {
        pk: { S: item.pk },
        sk: { S: item.sk },
        body: { B: new TextEncoder().encode(JSON.stringify(item.distributions)) },
        rev: { S: "0123456789abcdef" },
      },
    };
  }
}

const commands = {
  GetItemCommand: class {
    constructor(public input: any) {}
  },
};

it("reads the targets the Go ledger writes for the bootstrap and for the project", async () => {
  const targets = await targetsOf(
    new TableAsTheLedgerWritesIt(),
    commands,
    written.table,
    written.class,
    written.project,
  );

  expect(targets).toEqual(written.items.flatMap((item) => item.distributions).sort());
});
