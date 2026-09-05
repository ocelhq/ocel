export const dynamic = "force-dynamic";

export default async function CatchAll({ params }: { params: Promise<{ parts: string[] }> }) {
  const { parts } = await params;
  return (
    <main>
      <h1>catch-all</h1>
      <p data-ocel="param:parts">{parts.map((part) => decodeURIComponent(part)).join("|")}</p>
    </main>
  );
}
