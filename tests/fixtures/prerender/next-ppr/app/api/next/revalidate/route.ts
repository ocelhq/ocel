import { revalidatePath, revalidateTag } from "next/cache";

export async function POST(request: Request) {
  const asked = new URL(request.url).searchParams;
  const tag = asked.get("tag");
  const path = asked.get("path");
  if (tag) {
    revalidateTag(tag, "max");
  } else if (path) {
    revalidatePath(path);
  } else {
    return Response.json({ error: "name a tag or a path to revalidate" }, { status: 400 });
  }
  return Response.json({ revalidated: tag ?? path });
}
