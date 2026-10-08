import { pageMarkdown, source } from "@/lib/source";

export const revalidate = false;

export async function GET() {
  const pages = await Promise.all(source.getPages().map(pageMarkdown));
  return new Response(pages.join("\n\n"), {
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}
