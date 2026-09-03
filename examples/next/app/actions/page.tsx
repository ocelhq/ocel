import { bump } from "../../lib/state";

export const dynamic = "force-dynamic";

async function record(formData: FormData) {
  "use server";
  await bump(`action:${String(formData.get("note") ?? "")}`);
}

export default function ActionsPage() {
  return (
    <main>
      <h1>actions</h1>
      <p data-ocel="page">actions</p>
      <form action={record}>
        <input name="note" defaultValue="" />
        <button type="submit">record</button>
      </form>
    </main>
  );
}
