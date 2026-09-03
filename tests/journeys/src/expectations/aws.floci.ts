import { DESTROY_TITLE, planTests, REDEPLOY_TITLE, REFUSE_TITLE, ROLLBACK_TITLE, UP_TITLE } from "../plan";
import { type Leg, specForTarget } from "../spec";
import type { Expectations } from "./types";

const NO_MASTER_SECRET = "https://github.com/ocelhq/ocel/issues/884";
const STAGE_VARIABLES = "https://github.com/ocelhq/ocel/issues/854";
const NO_STREAMED_BODY = "https://github.com/ocelhq/ocel/issues/851";
const CLOUDFRONT_STUB = "https://github.com/ocelhq/ocel/issues/852";
const NO_CLOUDFLARE_API = "https://github.com/ocelhq/ocel/issues/860";
const SST_UTIL = "https://github.com/ocelhq/ocel/issues/857";
const PULUMI_SERIALIZATION = "https://github.com/ocelhq/ocel/issues/856";

const LEGS: Leg[] = ["up", "contract", "redeploy", "rollback", "destroy"];

const LEG_TITLES = new Set([UP_TITLE, REDEPLOY_TITLE, ROLLBACK_TITLE, DESTROY_TITLE]);

const STREAM_ROW = "GET /api/probes/stream streams its chunks in order to the sentinel";

export const EDGE_ENV = "OCEL_AWS_EDGE";

const DEFAULT_EDGE = "cloudfront";

const LADDER_ISSUES: Record<string, string> = {
  "with-sst": SST_UTIL,
  "with-pulumi": PULUMI_SERIALIZATION,
};

function ladderIssueFor(cell: string): string | undefined {
  const [example] = cell.split("/");
  return example ? LADDER_ISSUES[example] : undefined;
}

function listing(issueFor: (title: string) => string): Expectations {
  const listed: Expectations = {};
  for (const test of planTests(specForTarget("aws"), LEGS)) {
    const issue = ladderIssueFor(test.cell);
    if (issue) {
      if (test.title !== REFUSE_TITLE) {
        (listed[test.cell] ??= {})[test.title] = issue;
      }
      continue;
    }
    (listed[test.cell] ??= {})[test.title] = issueFor(test.title);
  }
  return listed;
}

const byEdge: Record<string, () => Expectations> = {
  "api-gateway": () =>
    listing((title) => {
      if (LEG_TITLES.has(title)) {
        return NO_MASTER_SECRET;
      }
      return title.endsWith(STREAM_ROW) ? NO_STREAMED_BODY : STAGE_VARIABLES;
    }),
  cloudfront: () => listing(() => CLOUDFRONT_STUB),
  cloudflare: () => listing(() => NO_CLOUDFLARE_API),
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
