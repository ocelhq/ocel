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

export class BootstrapError extends Error {
  constructor(message: string, readonly detail?: unknown) {
    super(message);
    this.name = "BootstrapError";
  }
}

export const BOOTSTRAP_MESSAGE = "The image optimizer could not serve this request.";
