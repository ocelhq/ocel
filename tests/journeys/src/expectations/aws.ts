import { contractTitle, UP_TITLE } from "../plan";
import { exampleOf, forEdge, ladderIssueFor, upTests } from "./edge";
import { CONTRACT_LEGS, legTitles } from "./keys";
import type { Expectations } from "./types";

const MIGRATE_NEEDS_LINK = "https://github.com/ocelhq/ocel/issues/911";
const BUILD_NEEDS_POSTGRES = "https://github.com/ocelhq/ocel/issues/849";
const DUPLICATE_ENV_KEY = "https://github.com/ocelhq/ocel/issues/907";
const NO_EDGE_CACHE = "https://github.com/ocelhq/ocel/issues/906";
const CLOUDFRONT_UP = "https://github.com/ocelhq/ocel/issues/923";
const CLOUDFLARE_UP = "https://github.com/ocelhq/ocel/issues/922";
const LINK_QUERY_HANGS = "https://github.com/ocelhq/ocel/issues/925";
const STALE_RELEASE_AFTER_DEPLOY = "https://github.com/ocelhq/ocel/issues/926";

const LINK_QUERY_ROW = "GET /api/link/query answers ok after a select through the link";
const LINK_ROW = "GET /api/link answers with what it resolved and the greeting it deployed with";

const API_GATEWAY_CONTRACT: Record<string, Record<string, string>> = {
  "with-transforms": {
    ...legTitles(LINK_QUERY_ROW, CONTRACT_LEGS, LINK_QUERY_HANGS),
    [contractTitle("redeploy", LINK_ROW)]: STALE_RELEASE_AFTER_DEPLOY,
  },
};

const EVERY_EDGE_UP: Record<string, string> = {
  express: MIGRATE_NEEDS_LINK,
  hono: MIGRATE_NEEDS_LINK,
  fastify: MIGRATE_NEEDS_LINK,
  next: BUILD_NEEDS_POSTGRES,
  workspace: DUPLICATE_ENV_KEY,
};

const API_GATEWAY_UP: Record<string, string> = {
  "hello-next": NO_EDGE_CACHE,
};

function upOnly(
  named: Record<string, string>,
  rest?: string,
  contract: Record<string, Record<string, string>> = {},
): Expectations {
  const listed: Expectations = {};
  for (const test of upTests()) {
    const example = exampleOf(test.cell);
    const issue = ladderIssueFor(test.cell) ?? EVERY_EDGE_UP[example] ?? named[example] ?? rest;
    listed[test.cell] = issue ? { [UP_TITLE]: issue } : { ...contract[example] };
  }
  return listed;
}

const byEdge: Record<string, () => Expectations> = {
  "api-gateway": () => upOnly(API_GATEWAY_UP, undefined, API_GATEWAY_CONTRACT),
  cloudfront: () => upOnly({}, CLOUDFRONT_UP),
  cloudflare: () => upOnly({}, CLOUDFLARE_UP),
};

export function aws(edge: string | undefined): Expectations {
  return forEdge(byEdge, edge);
}
