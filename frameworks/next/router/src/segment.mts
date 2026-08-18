const SEGMENT_PREFETCH = "next-router-segment-prefetch";
const POSTPONED = "x-nextjs-postponed";
const SEGMENT_PAYLOAD = "2";

export function isSegmentPrefetch(headers: Headers): boolean {
  return headers.get("RSC") !== null && headers.get(SEGMENT_PREFETCH) !== null;
}

export function isSegmentPayload(response: Response): boolean {
  return (
    response.status === 204 ||
    response.headers.get(POSTPONED) === SEGMENT_PAYLOAD
  );
}

export function asSegmentPayload(response: Response): Response {
  if (!response.ok || isSegmentPayload(response)) return response;
  response.body?.cancel();
  const headers = new Headers();
  const vary = response.headers.get("vary");
  if (vary) headers.set("vary", vary);
  return new Response(null, { status: 204, headers });
}
