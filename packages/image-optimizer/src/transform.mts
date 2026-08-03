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

// The passthrough rules and the transform, in the order the design fixes them.
// The order is not a style choice: CVE-2025-55173 was an ordering defect, where
// a bypass branch ran ahead of the not-an-image rejection and a payload that
// was never an image reached a response body under a type it chose.

// libvips gets seven seconds and then the operation is abandoned. The default
// is no timeout at all, and a 200-byte SVG with a large enough feMorphology
// radius will occupy a worker until Lambda kills the container.
const SHARP_TIMEOUT_SECONDS = 7;

// sharp's own default. Named here so it is a decision rather than an omission:
// raising it (or the `unlimited: true` that removes it) hands an attacker the
// difference between a 1 MB file and a 100 000 x 100 000 pixel decode.
const LIMIT_INPUT_PIXELS = 268402689;

export interface Transformed {
  bytes: Uint8Array;
  contentType: string;
  // Bytes that were never transformed. Both the deliberate bypasses and the
  // failure fallback are unmodified, and both take the upstream etag.
  unmodified: boolean;
  // The failure fallback specifically. Only this one forces the edge to
  // minimumCacheTTL, so an animated GIF — which is unmodified but perfectly
  // well served — keeps its upstream freshness.
  passthrough: boolean;
}

export interface TransformInput {
  bytes: Uint8Array;
  // The type the edge negotiated out of Accept. Empty when Accept negotiated
  // nothing, which is the common case for `*/*` and `image/*`. Anything else has
  // to be one of `config.formats` — see outputType.
  mimeType: string;
  width: number;
  quality: number;
  config: CompiledImageConfig;
}

export async function transform(input: TransformInput): Promise<Transformed> {
  const { bytes, config } = input;

  // Rule 1. From magic bytes only, and before anything else looks at this
  // payload — including any branch that would return it unmodified.
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

  // Rule 2. SVG is markup with script in it; serving it from the app's own
  // origin is a stored XSS unless the app has said otherwise.
  if (upstreamType.startsWith("image/svg") && !config.dangerouslyAllowSVG) {
    throw new ImageError(
      400,
      '"url" parameter is valid but image type is not allowed',
      { upstreamType },
    );
  }

  // Rule 3. A transform would flatten an animation to its first frame, which is
  // worse than not optimizing it.
  if (ANIMATABLE_TYPES.includes(upstreamType) && isAnimated(bytes, upstreamType)) {
    return { bytes, contentType: upstreamType, unmodified: true, passthrough: false };
  }

  // Rule 4.
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

// Both branches of the failure rule. The 400 branch is unreachable from
// transform() above, because rule 1 has already rejected every input with no
// detected type — which is exactly the ordering CVE-2025-55173 was about, and
// is worth keeping structurally impossible rather than merely unused. It stays
// a branch here so the rule is stated once and can be exercised directly.
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

// The negotiated type wins, but only after it is checked — it is the one payload
// field that reaches a response header verbatim, and the edge holds zero
// authority, so taking it on the caller's word was the single place that rule
// was untrue. Unchecked, `mimeType: text/html` produced a 200 whose
// Content-Type was text/html over JPEG bytes, and `image/avif` was honoured
// against a config whose `formats` listed only webp — so tightening `formats`
// had no effect here.
//
// A value outside `'' ∪ config.formats` cannot come from a client: the edge
// negotiates against this same config and only ever sends
// `'' | image/avif | image/webp`. It means the two tiers disagree, which is the
// substrate failing rather than the request being wrong — so it lands on 502,
// the status the edge refuses to cache, not on a 400 it would hold.
//
// Failing that, the source's own type is kept when it is one we have an
// extension for and is not itself an output-only format — re-encoding webp as
// webp or avif as avif without a negotiation to justify it would hand back a
// format the client never said it accepts. Everything else becomes JPEG.
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
    // Left at sharp's default rather than forced off: sequential reads are what
    // keep a large source from being fully materialised in memory, and
    // `sequentialRead: false` is a memory limit an attacker picks the shape of.
    // `failOn` is likewise untouched, so a truncated or corrupt input fails
    // instead of being handed to the encoder as whatever libvips salvaged.
    // Nothing here takes `density` from the request; an attacker-set density is
    // an SVG rasterised at any resolution they like.
  })
    .timeout({ seconds: SHARP_TIMEOUT_SECONDS })
    // Before the resize, so EXIF orientation is applied to the source and the
    // requested width means the width the browser will see.
    .rotate()
    .resize(width, undefined, { withoutEnlargement: true });

  if (contentType === AVIF) {
    // AVIF at a given quality number is visually far ahead of JPEG at the same
    // number, so Next rescales the app's quality onto AVIF's curve rather than
    // spending encode time on a fidelity nobody asked for. The rescale is a flat
    // -20, floored at 1: Next 16.2.10's own `image-optimizer.js:885`.
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
