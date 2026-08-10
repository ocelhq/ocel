import type { CompiledImageConfig } from "./contract.mjs";
import { ImageError, SubstrateError } from "./errors.mjs";
import { sharp } from "./sharp.mjs";
import {
  ANIMATABLE_TYPES,
  AVIF,
  BYPASS_TYPES,
  JPEG,
  PNG,
  WEBP,
  detectContentType,
  extensionFor,
  isAnimated,
} from "./sniff.mjs";

const SHARP_TIMEOUT_SECONDS = 7;

const LIMIT_INPUT_PIXELS = 268402689;

export interface Transformed {
  bytes: Uint8Array;
  contentType: string;
  unmodified: boolean;
  passthrough: boolean;
}

export interface TransformInput {
  bytes: Uint8Array;
  mimeType: string;
  width: number;
  quality: number;
  config: CompiledImageConfig;
}

export async function transform(input: TransformInput): Promise<Transformed> {
  const { bytes, config } = input;

  const upstreamType = detectContentType(bytes);
  if (
    !upstreamType ||
    !upstreamType.startsWith("image/") ||
    upstreamType.includes(",")
  ) {
    throw new ImageError(400, "The requested resource isn't a valid image.", {
      upstreamType,
    });
  }

  if (upstreamType.startsWith("image/svg") && !config.dangerouslyAllowSVG) {
    throw new ImageError(
      400,
      '"url" parameter is valid but image type is not allowed',
      { upstreamType },
    );
  }

  if (ANIMATABLE_TYPES.includes(upstreamType) && isAnimated(bytes, upstreamType)) {
    return { bytes, contentType: upstreamType, unmodified: true, passthrough: false };
  }

  if (BYPASS_TYPES.includes(upstreamType)) {
    return { bytes, contentType: upstreamType, unmodified: true, passthrough: false };
  }

  const contentType = outputType(input.mimeType, upstreamType, config);
  try {
    return {
      bytes: await encode(input, contentType),
      contentType,
      unmodified: false,
      passthrough: false,
    };
  } catch (error) {
    console.error("ocel image optimizer: transform failed", error);
    return fallbackOr400(bytes, upstreamType, error);
  }
}

export function fallbackOr400(
  bytes: Uint8Array,
  upstreamType: string | null,
  detail: unknown,
): Transformed {
  if (!upstreamType) {
    throw new ImageError(
      400,
      "Unable to optimize image and unable to fallback to upstream image",
      detail,
    );
  }
  return { bytes, contentType: upstreamType, unmodified: true, passthrough: true };
}

function outputType(
  mimeType: string,
  upstreamType: string,
  config: CompiledImageConfig,
): string {
  if (mimeType) {
    if (!config.formats.includes(mimeType)) {
      throw new SubstrateError("mimeType is not a configured output format", mimeType);
    }
    return mimeType;
  }
  if (extensionFor(upstreamType) && upstreamType !== WEBP && upstreamType !== AVIF) {
    return upstreamType;
  }
  return JPEG;
}

async function encode(input: TransformInput, contentType: string): Promise<Uint8Array> {
  const { bytes, width, quality } = input;
  const pipeline = sharp(bytes, {
    limitInputPixels: LIMIT_INPUT_PIXELS,
  })
    .timeout({ seconds: SHARP_TIMEOUT_SECONDS })
    .rotate()
    .resize(width, undefined, { withoutEnlargement: true });

  if (contentType === AVIF) {
    pipeline.avif({ quality: Math.max(quality - 20, 1), effort: 3 });
  } else if (contentType === WEBP) {
    pipeline.webp({ quality });
  } else if (contentType === PNG) {
    pipeline.png({ quality });
  } else if (contentType === JPEG) {
    pipeline.jpeg({ quality, mozjpeg: true });
  }

  return new Uint8Array(await pipeline.toBuffer());
}
