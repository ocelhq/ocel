import { llmsIndex } from "@/lib/llms";
import { source } from "@/lib/source";

export const revalidate = false;

export function GET() {
  const index = llmsIndex(source.getPageTree(), {
    title: "Ocel",
    summary: source.getPage([])?.data.description ?? "",
  });
  return new Response(index, { headers: { "content-type": "text/plain; charset=utf-8" } });
}
