import { cacheLife } from "next/cache";
import { Suspense } from "react";

async function token() {
  "use cache";
  cacheLife({ stale: 5, revalidate: 5, expire: 300 });
  return Date.now();
}

async function IsrToken() {
  return <p id="isr-token">isr-token:{await token()}</p>;
}

export default function IsrPage() {
  return (
    <Suspense fallback={<p>loading</p>}>
      <IsrToken />
    </Suspense>
  );
}
