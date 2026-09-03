import { EDGE_ISR_TITLE } from "../nextCache";
import type { Leg } from "../spec";
import { legTitles } from "./rows";
import type { Expectations } from "./types";

const EDGE_RUNTIME_ISR = "https://github.com/ocelhq/ocel/issues/899";

const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

export const aws: Expectations = {
  "next/web": legTitles(EDGE_ISR_TITLE, CONTRACT_LEGS, EDGE_RUNTIME_ISR),
};
