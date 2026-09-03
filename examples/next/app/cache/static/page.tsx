import { Stamp } from "../../../components/stamp";

export const dynamic = "force-static";
export const revalidate = false;

export default async function CacheStatic() {
  const at = new Date().toISOString();
  return (
    <main>
      <h1>cache static</h1>
      <Stamp scope="static" cached={at} />
    </main>
  );
}
