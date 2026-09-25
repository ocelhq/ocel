"use server";

import {
  type Connector as Dialled,
  envvars,
  type Refusal,
  statusOf,
  ValueError,
} from "@console/connectors";
import { db } from "@console/db";
import { type EnvironmentClass, project } from "@console/db/schema";
import type { Address, OtherValue, State, Version } from "@ui/vars";
import { and, eq } from "drizzle-orm";
import { requireOrganization } from "@/lib/access";
import { abilityFor, connectorFor, dial, noteDenial } from "@/lib/connectors";
import { latestTopology, namedEnvironments } from "@/lib/project-variables";
import { stateOf } from "@/lib/variables";

export type Answer<T> = { ok: true; result: T } | { ok: false; status: number; message: string };

function failed(status: number, message: string): Answer<never> {
  return { ok: false, status, message };
}

async function refused(
  connectorId: string,
  verb: string,
  refusal: Refusal,
): Promise<Answer<never>> {
  await noteDenial(connectorId, verb, refusal);
  return failed(statusOf(refusal), refusal.message);
}

interface Reached {
  slug: string;
  projectId: string;
  connector: Dialled;
  held: EnvironmentClass;
}

async function reach(projectId: string, env: string): Promise<Answer<Reached>> {
  const held: EnvironmentClass = env === "preview" ? "preview" : "production";
  const session = await requireOrganization();
  const [found] = await db
    .select({ id: project.id, slug: project.slug })
    .from(project)
    .where(
      and(eq(project.organizationId, session.activeOrganizationId), eq(project.id, projectId)),
    );
  if (!found) {
    return failed(404, "no such project");
  }
  const latest = await latestTopology(found.id, held);
  if (latest.error || latest.row === null) {
    return failed(409, "this project has reported no deploy, so nothing names its variables");
  }
  const connector = await connectorFor(session.activeOrganizationId, latest.row.target);
  if (connector === null) {
    return failed(409, "no connector runs in the account that holds these values");
  }
  const dialled = await dial(session, connector);
  if (dialled === null) {
    return failed(409, "this connector publishes no address the console can reach");
  }
  return { ok: true, result: { slug: found.slug, projectId: found.id, connector: dialled, held } };
}

async function attempt<T>(run: () => Promise<Answer<T>>): Promise<Answer<T>> {
  try {
    return await run();
  } catch (thrown) {
    if (thrown instanceof ValueError) {
      return failed(thrown.status, thrown.message);
    }
    return failed(502, thrown instanceof Error ? thrown.message : String(thrown));
  }
}

export async function readState(projectId: string, env: string): Promise<Answer<State>> {
  return attempt(async () => {
    const held: EnvironmentClass = env === "preview" ? "preview" : "production";
    const session = await requireOrganization();
    const [found] = await db
      .select({ id: project.id, slug: project.slug })
      .from(project)
      .where(
        and(eq(project.organizationId, session.activeOrganizationId), eq(project.id, projectId)),
      );
    if (!found) {
      return failed(404, "no such project");
    }
    const latest = await latestTopology(found.id, held);
    if (latest.error || latest.row === null) {
      return failed(409, "this project has reported no deploy");
    }
    const environments = await namedEnvironments(found.id);
    const connector = await connectorFor(session.activeOrganizationId, latest.row.target);
    const can = await abilityFor(session, connector);
    const dialled = connector === null ? null : await dial(session, connector);
    if (dialled === null) {
      return {
        ok: true,
        result: stateOf(found.slug, held, latest.row.topology, [], environments, can, "unknown"),
      };
    }
    const answer = await envvars.list(dialled, held, found.slug);
    if (!answer.done) {
      return refused(dialled.id, "list", answer.refusal);
    }
    const described = await envvars.describeEnvSource(dialled, held, found.slug);
    if (!described.done) {
      return refused(dialled.id, "describe", described.refusal);
    }
    return {
      ok: true,
      result: stateOf(
        found.slug,
        held,
        latest.row.topology,
        answer.result,
        environments,
        can,
        "live",
        described.result,
      ),
    };
  });
}

export async function revealValues(
  projectId: string,
  env: string,
  cells: Address[],
): Promise<Answer<{ values: (Address & { value: string })[]; errors: [] }>> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const answer = await envvars.reveal(connector, held, slug, cells);
    if (!answer.done) {
      return refused(connector.id, "reveal", answer.refusal);
    }
    return { ok: true, result: { values: answer.result, errors: [] } };
  });
}

export async function setValue(
  projectId: string,
  env: string,
  at: Address,
  value: string,
  version: number,
): Promise<Answer<null>> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const answer = await envvars.set(connector, held, slug, at, value, version);
    if (!answer.done) {
      return refused(connector.id, "set", answer.refusal);
    }
    return { ok: true, result: null };
  });
}

export async function createValue(
  projectId: string,
  env: string,
  at: Address,
  value: string,
): Promise<Answer<{ awaitingApproval: boolean }>> {
  return attempt(async () => {
    if (at.environment !== "") {
      return failed(
        400,
        `a value for ${at.environment} is ocel's own to hold, never the env source's`,
      );
    }
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, projectId: id, connector, held } = reached.result;
    const latest = await latestTopology(id, held);
    const description =
      (latest.error ? undefined : latest.row?.topology.apps)
        ?.flatMap((app) => app.variables)
        .find((variable) => variable.key === at.key)?.description ?? "";
    const answer = await envvars.createInEnvSource(connector, held, slug, at, value, description);
    if (!answer.done) {
      return refused(connector.id, "create", answer.refusal);
    }
    return { ok: true, result: answer.result };
  });
}

export async function removeValue(
  projectId: string,
  env: string,
  at: Address,
  version: number,
): Promise<Answer<null>> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const answer = await envvars.remove(connector, held, slug, at, version);
    if (!answer.done) {
      return refused(connector.id, "remove", answer.refusal);
    }
    return { ok: true, result: null };
  });
}

export async function listVersions(
  projectId: string,
  env: string,
  at: Address,
): Promise<Answer<Version[]>> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const answer = await envvars.versions(connector, held, slug, at);
    if (!answer.done) {
      return refused(connector.id, "versions", answer.refusal);
    }
    return { ok: true, result: answer.result };
  });
}

export async function otherValues(
  projectId: string,
  env: string,
): Promise<Answer<{ tier: string; values: OtherValue[] }>> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const other: EnvironmentClass = held === "production" ? "preview" : "production";
    const listed = await envvars.list(connector, other, slug);
    if (!listed.done) {
      return refused(connector.id, "list", listed.refusal);
    }
    const readable = listed.result.filter((value) => value.reference === undefined);
    const shown = await envvars.reveal(connector, other, slug, readable);
    const held_values = new Map(
      shown.done
        ? shown.result.map((value) => [
            `${value.key} ${value.folder} ${value.environment}`,
            value.value,
          ])
        : [],
    );
    return {
      ok: true,
      result: {
        tier: other,
        values: listed.result.map((value) => ({
          key: value.key,
          folder: value.folder,
          environment: value.environment,
          version: value.version,
          class: "plain" as const,
          ...(value.reference && { reference: value.reference }),
          ...(held_values.has(`${value.key} ${value.folder} ${value.environment}`) && {
            value: held_values.get(`${value.key} ${value.folder} ${value.environment}`),
          }),
          ...(!shown.done && { error: shown.refusal.message }),
        })),
      },
    };
  });
}

export async function copyValues(
  projectId: string,
  env: string,
  cells: (Address & { version: number })[],
): Promise<
  Answer<{ results: (Address & { saved: boolean; conflict?: boolean; error?: string })[] }>
> {
  return attempt(async () => {
    const reached = await reach(projectId, env);
    if (!reached.ok) return reached;
    const { slug, connector, held } = reached.result;
    const other: EnvironmentClass = held === "production" ? "preview" : "production";
    const shown = await envvars.reveal(connector, other, slug, cells);
    if (!shown.done) {
      return refused(connector.id, "reveal", shown.refusal);
    }
    const source = new Map(
      shown.result.map((value) => [
        `${value.key} ${value.folder} ${value.environment}`,
        value.value,
      ]),
    );
    const results = [];
    for (const at of cells) {
      const value = source.get(`${at.key} ${at.folder} ${at.environment}`);
      if (value === undefined) {
        results.push({ ...at, saved: false, error: `${other} holds no value for ${at.key}` });
        continue;
      }
      try {
        const answer = await envvars.set(connector, held, slug, at, value, at.version);
        if (!answer.done) {
          await noteDenial(connector.id, "set", answer.refusal);
        }
        results.push(
          answer.done
            ? { ...at, saved: true }
            : { ...at, saved: false, error: answer.refusal.message },
        );
      } catch (thrown) {
        results.push({
          ...at,
          saved: false,
          ...(thrown instanceof ValueError && thrown.status === 409 && { conflict: true }),
          error: thrown instanceof Error ? thrown.message : String(thrown),
        });
      }
    }
    return { ok: true, result: { results } };
  });
}
