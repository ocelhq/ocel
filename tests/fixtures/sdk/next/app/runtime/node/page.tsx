import { Stamp } from "../../../components/stamp";
import { bootId } from "../../../lib/boot";
import { bump } from "../../../lib/state";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export default async function NodeRuntime() {
  const boot = bootId();
  await bump(`request:${boot}`);
  return (
    <main>
      <h1>node runtime</h1>
      <p data-ocel="boot">{boot}</p>
      <Stamp
        scope="runtime"
        cached={process.env.NEXT_RUNTIME ?? "unknown"}
        live={new Date().toISOString()}
      />
    </main>
  );
}
