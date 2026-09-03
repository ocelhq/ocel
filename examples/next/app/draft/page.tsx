import { draftMode } from "next/headers";
import { Stamp } from "../../components/stamp";

export default async function Draft() {
  const { isEnabled } = await draftMode();
  const at = new Date().toISOString();
  return (
    <main>
      <h1>draft</h1>
      <p data-ocel="draft">{isEnabled ? "enabled" : "disabled"}</p>
      <Stamp scope="draft" cached={at} live={at} />
    </main>
  );
}
