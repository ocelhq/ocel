type BodyInit = NonNullable<RequestInit["body"]>;

export interface OriginBodyBudget {
  maxBytes: number;
}

export function originBodyBudget(
  maxBytes: string | undefined,
): OriginBodyBudget | undefined {
  const max = Number(maxBytes);
  if (!Number.isFinite(max) || max <= 0) return undefined;
  return { maxBytes: max };
}

export function payloadTooLarge(): Response {
  return new Response(null, { status: 413 });
}

export async function bodyWithinBudget(
  request: Request,
  budget: OriginBodyBudget,
): Promise<BodyInit | null | undefined> {
  const declared = Number(request.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > 0) {
    return declared > budget.maxBytes ? undefined : request.body;
  }
  const buffered = await request.arrayBuffer();
  return buffered.byteLength > budget.maxBytes ? undefined : buffered;
}
