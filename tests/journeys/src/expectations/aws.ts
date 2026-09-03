import { EDGE_ISR_TITLE } from "../nextCache";
import { CONTRACT_LEGS, legTitles } from "./keys";
import type { Expectations } from "./types";

const EDGE_RUNTIME_ISR = "https://github.com/ocelhq/ocel/issues/899";

export const aws: Expectations = {
  "next/web": legTitles(EDGE_ISR_TITLE, CONTRACT_LEGS, EDGE_RUNTIME_ISR),
};
