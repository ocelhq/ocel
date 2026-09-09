export const EMPTY_BODY_HEADER = "x-ocel-empty-body";

export function dropEmptyBodySentinel(response: Response): Response {
  if (!response.headers.has(EMPTY_BODY_HEADER)) return response;
  void response.body?.cancel();
  const restored = new Response(null, response);
  restored.headers.delete(EMPTY_BODY_HEADER);
  return restored;
}
