import { connection } from "next/server";
import { Suspense } from "react";

async function Stamp() {
  "use cache";
  return <span data-ocel="warm:cached">{new Date().toISOString()}</span>;
}

async function Hole() {
  await connection();
  return <span data-ocel="warm:live">{new Date().toISOString()}</span>;
}

export default function Warm() {
  return (
    <main>
      <h1>warm</h1>
      <Stamp />
      <Suspense fallback={<span data-ocel="warm:waiting">waiting</span>}>
        <Hole />
      </Suspense>
    </main>
  );
}
