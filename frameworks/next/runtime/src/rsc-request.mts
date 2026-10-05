import type { RequestHeaders } from "./request-headers.mjs";

const rscRequestKey = Symbol.for("ocel.rsc-request");

export function noteRscRequest(headers: RequestHeaders): void {
  if (headers.rsc === "1") headers[rscRequestKey] = true;
}

export function isRscRequest(headers: RequestHeaders): boolean {
  return headers[rscRequestKey] === true || headers.rsc === "1";
}
