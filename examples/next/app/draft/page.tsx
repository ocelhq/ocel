import { draftMode } from "next/headers";

export default async function Draft() {
  const { isEnabled } = await draftMode();
  return (
    <main>
      <h1>draft</h1>
      <p data-ocel="draft">{isEnabled ? "enabled" : "disabled"}</p>
    </main>
  );
}
