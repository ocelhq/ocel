import { connection } from "next/server";
import { Suspense } from "react";
import { ShellStamp } from "../../components/shell-stamp";

async function Hole() {
  await connection();
  return <span data-ocel="ppr:live">{new Date().toISOString()}</span>;
}

export default function Ppr() {
  return (
    <main>
      <h1>ppr</h1>
      <ShellStamp scope="ppr" tag="ppr-shell" />
      <Suspense fallback={<span data-ocel="ppr:waiting">waiting</span>}>
        <Hole />
      </Suspense>
    </main>
  );
}
