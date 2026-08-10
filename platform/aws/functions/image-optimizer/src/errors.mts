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

export function upstreamFailure(detail: unknown): ImageError {
  return new ImageError(
    400,
    '"url" parameter is valid but upstream response is invalid',
    detail,
  );
}

export class SubstrateError extends Error {
  constructor(message: string, readonly detail?: unknown) {
    super(message);
    this.name = "SubstrateError";
  }
}

export const SUBSTRATE_MESSAGE = "The image optimizer could not serve this request.";
