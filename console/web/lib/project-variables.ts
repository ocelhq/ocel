import { db } from "@console/db";
import { deployment, type EnvironmentClass } from "@console/db/schema";
import { and, desc, eq } from "drizzle-orm";
import type { Latest } from "@/lib/variables";

export async function latestTopology(
  projectId: string,
  held: EnvironmentClass,
): Promise<{ error: true } | { error: false; row: Latest | null }> {
  try {
    const [row] = await db
      .select({
        id: deployment.id,
        topology: deployment.topology,
        deployedAt: deployment.deployedAt,
        promotionId: deployment.promotionId,
        providerName: deployment.providerName,
        providerRegion: deployment.providerRegion,
        tag: deployment.tag,
      })
      .from(deployment)
      .where(
        and(
          eq(deployment.projectId, projectId),
          eq(deployment.environmentClass, held),
          eq(deployment.outcome, "succeeded"),
        ),
      )
      .orderBy(desc(deployment.deployedAt))
      .limit(1);
    return { error: false, row: row ?? null };
  } catch {
    return { error: true };
  }
}

export async function namedEnvironments(projectId: string): Promise<string[]> {
  try {
    const rows = await db
      .selectDistinct({ identity: deployment.environmentIdentity })
      .from(deployment)
      .where(and(eq(deployment.projectId, projectId), eq(deployment.environmentClass, "preview")));
    return rows.map((row) => row.identity).filter((identity) => identity !== "");
  } catch {
    return [];
  }
}
