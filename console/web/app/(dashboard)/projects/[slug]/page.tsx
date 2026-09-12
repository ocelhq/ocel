import { db } from "@console/db";
import { type Deployment, project } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { notFound } from "next/navigation";
import { requireOrganization } from "@/lib/access";
import { latestDeployments } from "@/lib/deployments";
import { environmentOf, withEnvironment } from "@/lib/environment";
import { tearsDown } from "@/lib/runs";
import type { Provenance } from "./overview/provenance";
import { ServiceMap } from "./overview/service-map";
import { LoadError, NeverDeployed, TornDown } from "./overview/states";

const canvas = "h-[calc(100svh-3.5rem)] min-h-0 overflow-hidden";

function provenanceOf(record: Deployment): Provenance {
  return {
    deployedAt: record.deployedAt.toISOString(),
    promotionId: record.promotionId,
    tag: record.tag,
    providerName: record.providerName,
    providerRegion: record.providerRegion,
  };
}

export default async function ProjectPage({
  params,
  searchParams,
}: {
  params: Promise<{ slug: string }>;
  searchParams: Promise<{ [key: string]: string | string[] | undefined }>;
}) {
  const [{ slug }, query, session] = await Promise.all([
    params,
    searchParams,
    requireOrganization(),
  ]);
  const environment = environmentOf(typeof query.env === "string" ? query.env : null);

  const [found] = await db
    .select({ id: project.id })
    .from(project)
    .where(and(eq(project.organizationId, session.activeOrganizationId), eq(project.slug, slug)));

  if (!found) {
    notFound();
  }

  const load = await latestDeployments(found.id, environment);
  const now = new Date().toISOString();

  if (load.error) {
    return (
      <div className={canvas}>
        <LoadError href={withEnvironment(`/projects/${slug}`, environment)} />
      </div>
    );
  }

  const { latest, lastPromoted } = load;

  if (!latest) {
    return (
      <div className={canvas}>
        <NeverDeployed environment={environment} now={now} />
      </div>
    );
  }

  if (latest.outcome === "succeeded" && tearsDown(latest.kind)) {
    return (
      <div className={canvas}>
        {lastPromoted ? (
          <TornDown topology={lastPromoted.topology} provenance={provenanceOf(latest)} now={now} />
        ) : (
          <NeverDeployed environment={environment} stamp={provenanceOf(latest)} now={now} />
        )}
      </div>
    );
  }

  if (latest.outcome === "failed") {
    const failure = { deployedAt: latest.deployedAt.toISOString(), error: latest.error };
    return (
      <div className={canvas}>
        {lastPromoted ? (
          <ServiceMap
            topology={lastPromoted.topology}
            provenance={provenanceOf(lastPromoted)}
            failure={failure}
            now={now}
          />
        ) : (
          <NeverDeployed environment={environment} failure={failure} now={now} />
        )}
      </div>
    );
  }

  return (
    <div className={canvas}>
      <ServiceMap topology={latest.topology} provenance={provenanceOf(latest)} now={now} />
    </div>
  );
}
