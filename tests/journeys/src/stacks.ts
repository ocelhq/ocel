import type { CheckContext } from "./checks/context";
import type { CellUnderTest } from "./run/cellRun";

export type StackPoint = "afterPublish" | "whileServing" | "afterOcelDestroy" | "afterStackDestroy";

export type StackCheck = {
  title: string;
  run: (cell: CellUnderTest, serving?: CheckContext) => Promise<void>;
};

export type StackChecks = Record<StackPoint, StackCheck[]>;

export interface ExternalStack {
  readonly checks: StackChecks;
  deploy(cell: CellUnderTest): Promise<void>;
  destroy(cell: CellUnderTest): Promise<void>;
  refuse(cell: CellUnderTest): Promise<void>;
  sweepStale(runId: string): Promise<void>;
  sweepRun(runId: string): Promise<void>;
}
