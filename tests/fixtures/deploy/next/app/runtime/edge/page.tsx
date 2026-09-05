import { Stamp } from "../../../components/stamp";

export const runtime = "edge";
export const dynamic = "force-dynamic";

export default function EdgeRuntime() {
  return (
    <main>
      <h1>edge runtime</h1>
      <Stamp
        scope="runtime"
        cached={process.env.NEXT_RUNTIME ?? "unknown"}
        live={new Date().toISOString()}
      />
    </main>
  );
}
