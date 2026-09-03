export const dynamic = "force-dynamic";

export default async function CacheDeployment() {
  return (
    <main>
      <h1>cache deployment</h1>
      <p data-ocel="deployment">{process.env.NEXT_DEPLOYMENT_ID ?? ""}</p>
    </main>
  );
}
