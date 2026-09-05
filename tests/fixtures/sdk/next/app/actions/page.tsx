import { record } from "./record";

export const dynamic = "force-dynamic";

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
