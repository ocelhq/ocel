import { cacheLife } from "next/cache";

async function token() {
  "use cache";
  cacheLife({ stale: 5, revalidate: 5, expire: 300 });
  return Date.now();
}

export default async function IsrPage() {
  return <p id="isr-token">isr-token:{await token()}</p>;
}
