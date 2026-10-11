import { cacheTag } from "next/cache";

export async function ShellStamp({ scope, tag }: { scope: string; tag: string }) {
  "use cache";
  cacheTag(tag);
  return <span data-ocel={`${scope}:cached`}>{new Date().toISOString()}</span>;
}
