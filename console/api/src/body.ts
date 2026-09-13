import type { ZodType } from "zod";

export type Read<T> = { ok: true; data: T } | { ok: false; refusal: Response };

export async function readBody<T>(request: Request, schema: ZodType<T>): Promise<Read<T>> {
  let raw: unknown;
  try {
    raw = await request.json();
  } catch {
    return {
      ok: false,
      refusal: Response.json({ error: "Invalid request body" }, { status: 400 }),
    };
  }
  const parsed = schema.safeParse(raw);
  if (!parsed.success) {
    return {
      ok: false,
      refusal: Response.json(
        { error: "Invalid request", issues: parsed.error.issues },
        { status: 400 },
      ),
    };
  }
  return { ok: true, data: parsed.data };
}
