import * as probes from "../../../../probes";
import type { RequestHandler } from "./$types";

const handled =
  (handler: (request: Request) => Response | Promise<Response>): RequestHandler =>
  ({ request }) =>
    handler(request);

export const GET = handled(probes.GET);
export const HEAD = handled(probes.HEAD);
export const POST = handled(probes.POST);
export const PUT = handled(probes.PUT);
export const PATCH = handled(probes.PATCH);
export const DELETE = handled(probes.DELETE);
export const OPTIONS = handled(probes.OPTIONS);
