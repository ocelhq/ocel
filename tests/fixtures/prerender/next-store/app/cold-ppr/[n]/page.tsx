import { connection } from "next/server";
import { Suspense } from "react";

export function generateStaticParams() {
  return [{ n: "1" }, { n: "2" }, { n: "3" }];
}

async function Hole() {
  await connection();
  return <span data-ocel="coldppr:live">{new Date().toISOString()}</span>;
}

export default async function ColdPpr({ params }: { params: Promise<{ n: string }> }) {
  const { n } = await params;
  return (
    <main>
      <h1>cold ppr {n}</h1>
      <span data-ocel="coldppr:static">prerendered at build</span>
      <Suspense fallback={<span data-ocel="coldppr:waiting">waiting</span>}>
        <Hole />
      </Suspense>
    </main>
  );
}
