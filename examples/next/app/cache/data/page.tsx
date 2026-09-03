import { Stamp } from "../../../components/stamp";
import { RESUME_TAG, upstreamCount } from "../../../lib/upstream";

export const dynamic = "force-dynamic";

export default async function CacheData() {
  const count = await upstreamCount("data", { next: { tags: [RESUME_TAG], revalidate: 3600 } });
  return (
    <main>
      <h1>cache data</h1>
      <Stamp scope="data" cached={count} live={new Date().toISOString()} />
    </main>
  );
}
