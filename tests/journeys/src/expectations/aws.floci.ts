import { cellKey, UP_TITLE } from "../plan";
import { specForTarget } from "../spec";
import type { Expectations } from "./types";

const NO_MASTER_SECRET = "https://github.com/ocelhq/ocel/issues/884";
const CLOUDFRONT_STUB = "https://github.com/ocelhq/ocel/issues/852";
const NO_CLOUDFLARE_API = "https://github.com/ocelhq/ocel/issues/860";

function everyCellAt(title: string, issue: string): Expectations {
  const listed: Expectations = {};
  for (const example of specForTarget("aws")) {
    for (const app of example.apps) {
      listed[cellKey(example.name, app)] = { [title]: issue };
    }
  }
  return listed;
}

const byEdge: Record<string, Expectations> = {
  "api-gateway": everyCellAt(UP_TITLE, NO_MASTER_SECRET),
  cloudfront: everyCellAt(UP_TITLE, CLOUDFRONT_STUB),
  cloudflare: everyCellAt(UP_TITLE, NO_CLOUDFLARE_API),
};

const DEFAULT_EDGE = "cloudfront";

function listedForEdge(edge: string): Expectations {
  const listed = byEdge[edge];
  if (!listed) {
    throw new Error(
      `OCEL_AWS_EDGE is ${edge}, and this file lists no cells for it (${Object.keys(byEdge).join(", ")})`,
    );
  }
  return listed;
}

export const awsFloci: Expectations = listedForEdge(process.env.OCEL_AWS_EDGE || DEFAULT_EDGE);
