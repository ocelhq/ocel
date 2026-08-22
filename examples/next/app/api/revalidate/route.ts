import { revalidateTag } from "next/cache";

export async function POST(request: Request) {
  const tag = new URL(request.url).searchParams.get("tag");
  if (!tag) {
    return Response.json({ error: "missing ?tag=" }, { status: 400 });
  }
  revalidateTag(tag, "max");
  return Response.json({ revalidated: tag });
}
