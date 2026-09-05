export const dynamic = "force-dynamic";

export default async function DynamicRoute({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  return (
    <main>
      <h1>dynamic</h1>
      <p data-ocel="param:slug">{decodeURIComponent(slug)}</p>
    </main>
  );
}
