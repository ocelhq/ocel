import { unstable_cache, updateTag } from "next/cache";
import { Stamp } from "../../../components/stamp";

export const dynamic = "force-static";
export const revalidate = 3600;

const renderedAt = unstable_cache(async () => new Date().toISOString(), ["cache-action"], {
  tags: ["cache-action"],
});

async function refresh() {
  "use server";
  updateTag("cache-action");
}

export default async function CacheAction() {
  return (
    <main>
      <h1>cache action</h1>
      <Stamp scope="action" cached={await renderedAt()} />
      <form action={refresh}>
        <button type="submit">refresh</button>
      </form>
    </main>
  );
}
