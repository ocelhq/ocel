import { notFound } from "next/navigation";
import { pageSlugs } from "@/lib/llms";
import { pageMarkdown, source } from "@/lib/source";

export const revalidate = false;

export async function GET(_request: Request, context: RouteContext<"/llms.mdx/docs/[[...slug]]">) {
  const { slug } = await context.params;
  const page = source.getPage(pageSlugs(slug));
  if (!page) notFound();

  return new Response(await pageMarkdown(page), {
    headers: { "content-type": "text/markdown; charset=utf-8" },
  });
}

export function generateStaticParams() {
  return source.generateParams().map((params) => ({ slug: [...params.slug, "content.md"] }));
}
