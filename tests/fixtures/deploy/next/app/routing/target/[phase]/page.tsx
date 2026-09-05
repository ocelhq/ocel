export const dynamic = "force-dynamic";

export default async function RewriteTarget({ params }: { params: Promise<{ phase: string }> }) {
  const { phase } = await params;
  return (
    <main>
      <h1>rewrite target</h1>
      <p data-ocel="page">{`target:${phase}`}</p>
    </main>
  );
}
