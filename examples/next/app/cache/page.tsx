import { cacheLife, cacheTag } from "next/cache";

async function cachedAt() {
  "use cache";
  cacheLife("editorial");
  cacheTag("example-cache");
  return new Date().toISOString();
}

export default async function CachePage() {
  return (
    <main>
      <h1>Cache Components</h1>
      <p>Cached at {await cachedAt()}</p>
      <p>
        POST <code>/api/revalidate?tag=example-cache</code> to revalidate this
        entry.
      </p>
    </main>
  );
}
