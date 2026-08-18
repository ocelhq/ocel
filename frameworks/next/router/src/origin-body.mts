export type OriginBodyEncoding = "identity" | "base64";

export interface OriginBodyBudget {
  maxBytes: number;
  encoding: OriginBodyEncoding;
}

const TEXT_CONTENT_TYPE =
  /^(text\/|application\/(json|javascript|xml|x-www-form-urlencoded)\b|[^;]+\+(json|xml)\b)/i;

export function originBodyBytes(
  byteLength: number,
  contentType: string | null,
  encoding: OriginBodyEncoding,
): number {
  if (encoding !== "base64") return byteLength;
  if (contentType !== null && TEXT_CONTENT_TYPE.test(contentType)) return byteLength;
  return Math.ceil(byteLength / 3) * 4;
}

export function originBodyBudget(
  maxBytes: string | undefined,
  encoding: string | undefined,
): OriginBodyBudget | undefined {
  const max = Number(maxBytes);
  if (!Number.isFinite(max) || max <= 0) return undefined;
  return { maxBytes: max, encoding: encoding === "base64" ? "base64" : "identity" };
}

export function payloadTooLarge(): Response {
  return new Response(null, { status: 413 });
}
