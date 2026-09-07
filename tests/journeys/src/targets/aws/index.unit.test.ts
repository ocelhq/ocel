import { describe, expect, it } from "bun:test";
import { type Cell, cellsOf, specByName, specForTarget } from "../../spec";
import { cellsBySlugPart, despite } from "./index";

const fixture = specByName("deploy", "node");

function named(name: string): Cell {
  return { name, fixture };
}

describe("cellsBySlugPart", () => {
  it("keys every cell the aws lane runs, and loses none of them", () => {
    const cells = specForTarget("aws").flatMap((row) => cellsOf(row, "aws"));

    expect(cellsBySlugPart(cells).size).toBe(cells.length);
  });

  it("refuses two cells that slug to one part, rather than dropping one", () => {
    expect(() =>
      cellsBySlugPart([named("deploy/node-api-gateway"), named("deploy/node/api-gateway")]),
    ).toThrow(/both slug to deploy-node-api-gateway/);
  });
});

describe("despite", () => {
  it("keeps a failure as a complaint and lets the work after it run", async () => {
    const complaints: string[] = [];
    const ran: string[] = [];

    for (const namespace of ["j-1799-half-deleted", "j-1799-standing"]) {
      await despite(complaints, `${namespace} sweep`, async () => {
        if (namespace.endsWith("half-deleted")) {
          throw new Error("the bootstrap stack is stuck in DELETE_FAILED");
        }
        ran.push(namespace);
      });
    }

    expect(ran).toEqual(["j-1799-standing"]);
    expect(complaints).toEqual([
      "j-1799-half-deleted sweep: Error: the bootstrap stack is stuck in DELETE_FAILED",
    ]);
  });
});
