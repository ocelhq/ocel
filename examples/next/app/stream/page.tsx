import { Suspense } from "react";
import { Deferred } from "../../components/deferred";

export const dynamic = "force-dynamic";

export default function StreamPage() {
  return (
    <main>
      <h1>stream</h1>
      <p data-ocel="page">ocel-shell</p>
      <Suspense fallback={<p>waiting</p>}>
        <Deferred />
      </Suspense>
    </main>
  );
}
