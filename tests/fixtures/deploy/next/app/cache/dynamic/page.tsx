import { Stamp } from "../../../components/stamp";

export const dynamic = "force-dynamic";

export default async function CacheDynamic() {
  const at = new Date().toISOString();
  return (
    <main>
      <h1>cache dynamic</h1>
      <Stamp scope="dynamic" cached={at} live={at} />
    </main>
  );
}
