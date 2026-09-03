import { Stamp } from "../../../components/stamp";

export const dynamic = "force-static";
export const revalidate = 5;

export default async function CacheIsr() {
  const at = new Date().toISOString();
  return (
    <main>
      <h1>cache isr</h1>
      <Stamp scope="isr" cached={at} live={at} />
    </main>
  );
}
