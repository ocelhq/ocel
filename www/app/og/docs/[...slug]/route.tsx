import { notFound } from "next/navigation";
import { ogImage } from "@/lib/og";
import { source } from "@/lib/source";

export const revalidate = false;

export async function GET(_request: Request, context: { params: Promise<{ slug: string[] }> }) {
  const { slug } = await context.params;
  const page = source.getPage(slug.slice(0, -1));
  if (!page) notFound();
  if (page.url === "/docs") {
    return ogImage({
      title: "Ocel Documentation",
      description: "Deploy apps to your own cloud or servers with one command.",
    });
  }
  return ogImage({ title: page.data.title, description: page.data.description });
}

export function generateStaticParams() {
  return source.getPages().map((page) => ({ slug: [...page.slugs, "image.png"] }));
}
