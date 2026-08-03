// Every failure this function can answer with, and the rule that the message a
// client sees is never the message an operator sees.
//
// The distinction is not cosmetic. This endpoint fetches arbitrary attacker-
// named URLs from inside a customer's AWS account, so any difference a caller
// can observe between "that host does not resolve", "that host resolved to a
// private address", "that host timed out" and "that host answered 403" is a
// port scanner. All of them collapse onto one status and one string; the cause
// goes to CloudWatch, which the attacker cannot read.

// A failure with a client-facing status and message. `detail` never leaves the
// process.
export class ImageError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly detail?: unknown,
  ) {
    super(message);
    this.name = "ImageError";
  }
}

// The single answer for every way fetching the source can fail. Next
// distinguishes 504 (timeout), 508 (redirect loop), 413 (too large) and the
// upstream's own status; each of those is an oracle bit about a network this
// caller cannot otherwise see, so they are one answer here. Next's own message
// text is kept because it is the one an app author already recognises.
export function upstreamFailure(detail: unknown): ImageError {
  return new ImageError(
    400,
    '"url" parameter is valid but upstream response is invalid',
    detail,
  );
}

// The substrate could not answer a well-formed request: the config is missing,
// does not hash to what the edge validated against, or something threw where
// nothing should. 502 rather than 500 because that is the status the edge
// refuses to cache — a transient inconsistency must not be frozen into the colo
// tier for minimumCacheTTL.
export class SubstrateError extends Error {
  constructor(message: string, readonly detail?: unknown) {
    super(message);
    this.name = "SubstrateError";
  }
}

export const SUBSTRATE_MESSAGE = "The image optimizer could not serve this request.";
