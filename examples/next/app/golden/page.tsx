import { cacheLife } from "next/cache";
import { Suspense } from "react";

async function GoldenBody() {
  "use cache";
  cacheLife({ stale: 3, revalidate: 3, expire: 300 });
  return (
    <main>
      <p id="golden-body">golden-body:v1</p>
      <p>
        Rendered identically whether or not the edge requested this page as a
        prefetch.
      </p>
    </main>
  );
}

export default function Page() {
  return (
    <Suspense fallback={<p>loading</p>}>
      <GoldenBody />
    </Suspense>
  );
}
