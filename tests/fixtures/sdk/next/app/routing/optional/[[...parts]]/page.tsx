export const dynamic = "force-dynamic";

export default async function OptionalCatchAll({
  params,
}: {
  params: Promise<{ parts?: string[] }>;
}) {
  const { parts } = await params;
  return (
    <main>
      <h1>optional catch-all</h1>
      <p data-ocel="param:parts">{parts?.length ? parts.map((part) => decodeURIComponent(part)).join("|") : "none"}</p>
    </main>
  );
}
