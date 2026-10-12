import { connection } from "next/server";
import { Suspense } from "react";
import { ShellStamp } from "../../../components/shell-stamp";

export function generateStaticParams() {
  return [{ id: "prebuilt" }];
}

async function Who({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  await connection();
  return (
    <>
      <span data-ocel="pprid:id">{id}</span>
      <span data-ocel="pprid:live">{new Date().toISOString()}</span>
    </>
  );
}

async function Param({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <span data-ocel="pprid:param">{id}</span>;
}

export default function PprById({ params }: { params: Promise<{ id: string }> }) {
  return (
    <main>
      <h1>ppr by id</h1>
      <ShellStamp scope="pprid" tag="pprid-shell" />
      <Suspense fallback={<span data-ocel="pprid:param-waiting">waiting</span>}>
        <Param params={params} />
      </Suspense>
      <Suspense fallback={<span data-ocel="pprid:waiting">waiting</span>}>
        <Who params={params} />
      </Suspense>
    </main>
  );
}
