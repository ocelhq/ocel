import { UPSTREAM_INVALID_MESSAGE } from "@framework/next-protocol/image";

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

const UPSTREAM_TIMED_OUT_MESSAGE = '"url" parameter is valid but upstream response timed out';

export function upstreamFailure(detail: unknown, status = 400): ImageError {
  return new ImageError(status, UPSTREAM_INVALID_MESSAGE, detail);
}

export function upstreamTimeout(detail: unknown): ImageError {
  return new ImageError(504, UPSTREAM_TIMED_OUT_MESSAGE, detail);
}

export class BootstrapError extends Error {
  constructor(
    message: string,
    readonly detail?: unknown,
  ) {
    super(message);
    this.name = "BootstrapError";
  }
}

export const BOOTSTRAP_MESSAGE = "The image optimizer could not serve this request.";
