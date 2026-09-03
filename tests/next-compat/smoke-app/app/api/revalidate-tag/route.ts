import { revalidateTag } from "next/cache";

export const dynamic = "force-dynamic";

export async function POST(request: Request): Promise<Response> {
  const tag = new URL(request.url).searchParams.get("tag");
  if (!tag) {
    return Response.json({ error: "no tag given" }, { status: 400 });
  }
  revalidateTag(tag, "default");
  return Response.json({ revalidated: tag, at: Date.now() });
}
