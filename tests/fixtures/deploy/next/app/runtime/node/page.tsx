import { Stamp } from "../../../components/stamp";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export default function NodeRuntime() {
  return (
    <main>
      <h1>node runtime</h1>
      <Stamp
        scope="runtime"
        cached={process.env.NEXT_RUNTIME ?? "unknown"}
        live={new Date().toISOString()}
      />
    </main>
  );
}
