import { Suspense } from "react";
import { Deferred } from "../../../components/deferred";
import { bump } from "../../../lib/state";

export const dynamic = "force-dynamic";

async function record(formData: FormData) {
  "use server";
  await bump(`action:${String(formData.get("note") ?? "")}`);
}

export default function StreamingActionPage() {
  return (
    <main>
      <h1>streaming action</h1>
      <p data-ocel="page">ocel-shell</p>
      <form action={record}>
        <input name="note" defaultValue="" />
        <button type="submit">record</button>
      </form>
      <Suspense fallback={<p>waiting</p>}>
        <Deferred />
      </Suspense>
    </main>
  );
}
