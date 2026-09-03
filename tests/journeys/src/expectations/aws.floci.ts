import { EDGE_ISR_TITLE } from "../nextCache";
import { DESTROY_TITLE, planTests, REDEPLOY_TITLE, ROLLBACK_TITLE, UP_TITLE } from "../plan";
import { specForTarget } from "../spec";
import { CONTRACT_LEGS } from "./keys";
import type { Expectations } from "./types";

const NO_MASTER_SECRET = "https://github.com/ocelhq/ocel/issues/884";
const STAGE_VARIABLES = "https://github.com/ocelhq/ocel/issues/854";
const NO_STREAMED_BODY = "https://github.com/ocelhq/ocel/issues/851";
const EDGE_RUNTIME_ISR = "https://github.com/ocelhq/ocel/issues/899";
const CLOUDFRONT_STUB = "https://github.com/ocelhq/ocel/issues/852";
const NO_CLOUDFLARE_API = "https://github.com/ocelhq/ocel/issues/904";
const SST_UTIL = "https://github.com/ocelhq/ocel/issues/857";
const PULUMI_SERIALIZATION = "https://github.com/ocelhq/ocel/issues/856";
const BUILD_NEEDS_POSTGRES = "https://github.com/ocelhq/ocel/issues/849";
const NO_EDGE_CACHE = "https://github.com/ocelhq/ocel/issues/906";
const DUPLICATE_ENV_KEY = "https://github.com/ocelhq/ocel/issues/907";

const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";

const LEG_MARKERS = new Set([UP_TITLE, DESTROY_TITLE, REDEPLOY_TITLE, ROLLBACK_TITLE]);

export const EDGE_ENV = "OCEL_AWS_EDGE";

const DEFAULT_EDGE = "cloudfront";

const LADDER_ISSUES: Record<string, string> = {
  "with-sst": SST_UTIL,
  "with-pulumi": PULUMI_SERIALIZATION,
};

const API_GATEWAY_UP: Record<string, string> = {
  express: NO_MASTER_SECRET,
  hono: NO_MASTER_SECRET,
  fastify: NO_MASTER_SECRET,
  "with-transforms": NO_MASTER_SECRET,
  next: BUILD_NEEDS_POSTGRES,
  "hello-next": NO_EDGE_CACHE,
  workspace: DUPLICATE_ENV_KEY,
};

function exampleOf(cell: string): string {
  const [example] = cell.split("/");
  return example ?? "";
}

function ladderIssueFor(cell: string): string | undefined {
  return LADDER_ISSUES[exampleOf(cell)];
}

function upOnly(issue: string): Expectations {
  const listed: Expectations = {};
  for (const test of planTests(specForTarget("aws"), ["up"])) {
    if (test.title !== UP_TITLE) {
      continue;
    }
    listed[test.cell] = { [UP_TITLE]: ladderIssueFor(test.cell) ?? issue };
  }
  return listed;
}

function contractIssueFor(title: string): string {
  if (title.endsWith(STREAM_ROW)) {
    return NO_STREAMED_BODY;
  }
  if (title.endsWith(EDGE_ISR_TITLE)) {
    return EDGE_RUNTIME_ISR;
  }
  return STAGE_VARIABLES;
}

function apiGateway(): Expectations {
  const listed: Expectations = {};
  for (const test of planTests(specForTarget("aws"), ["up"])) {
    if (test.title !== UP_TITLE) {
      continue;
    }
    const issue = ladderIssueFor(test.cell) ?? API_GATEWAY_UP[exampleOf(test.cell)];
    listed[test.cell] = issue ? { [UP_TITLE]: issue } : {};
  }
  for (const test of planTests(specForTarget("aws"), CONTRACT_LEGS)) {
    if (ladderIssueFor(test.cell) || LEG_MARKERS.has(test.title)) {
      continue;
    }
    const cell = listed[test.cell];
    if (cell) {
      cell[test.title] = contractIssueFor(test.title);
    }
  }
  return listed;
}

const byEdge: Record<string, () => Expectations> = {
  "api-gateway": apiGateway,
  cloudfront: () => upOnly(CLOUDFRONT_STUB),
  cloudflare: () => upOnly(NO_CLOUDFLARE_API),
};

export function awsFloci(edge: string | undefined): Expectations {
  const named = edge || DEFAULT_EDGE;
  const listed = byEdge[named];
  if (!listed) {
    throw new Error(
      `${EDGE_ENV} is ${named}, and this file lists no cells for it (${Object.keys(byEdge).join(", ")})`,
    );
  }
  return listed();
}
