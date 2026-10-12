import { unstable_cache } from "next/cache";
import { Stamp } from "../../../components/stamp";

export const dynamic = "force-static";
export const revalidate = 3600;

const renderedAt = unstable_cache(async () => new Date().toISOString(), ["cache-tagged"], {
  tags: ["cache-tagged"],
});

export default async function CacheTagged() {
  return (
    <main>
      <h1>cache tagged</h1>
      <Stamp scope="tagged" cached={await renderedAt()} />
    </main>
  );
}
