"use server";

import { envvars, type Stored, ValueError } from "@console/connectors";
import { db } from "@console/db";
import { type EnvironmentClass, project } from "@console/db/schema";
import type { Address, OtherValue, State, Version } from "@ui/vars";
import { and, eq } from "drizzle-orm";
import { requireOrganization } from "@/lib/access";
import { type Connector, connectorFor } from "@/lib/connectors";
import { latestTopology, namedEnvironments } from "@/lib/project-variables";
import { stateOf } from "@/lib/variables";

export type Answer<T> = { ok: true; result: T } | { ok: false; status: number; message: string };

function failed(status: number, message: string): Answer<never> {
  return { ok: false, status, message };
}

interface Reached {
  slug: string;
  projectId: string;
  connector: Connector;
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
  const connector = await connectorFor(session.activeOrganizationId, latest.row.providerName);
  if (connector === null) {
    return failed(409, "no connector runs in the account that holds these values");
  }
  return { ok: true, result: { slug: found.slug, projectId: found.id, connector, held } };
}

function dialled(connector: Connector) {
  return { id: connector.id, url: connector.url, capabilities: connector.capabilities };
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
    const connector = await connectorFor(session.activeOrganizationId, latest.row.providerName);
    let stored: readonly Stored[] = [];
    if (connector === null) {
      return {
        ok: true,
        result: stateOf(found.slug, held, latest.row.topology, [], environments, "unknown"),
      };
    }
    const answer = await envvars.list(dialled(connector), held, found.slug);
    if (!answer.done) {
      return failed(502, answer.refusal.message);
    }
    stored = answer.result;
    return {
      ok: true,
      result: stateOf(found.slug, held, latest.row.topology, stored, environments),
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
    const answer = await envvars.reveal(dialled(connector), held, slug, cells);
    if (!answer.done) {
      return failed(502, answer.refusal.message);
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
    const answer = await envvars.set(dialled(connector), held, slug, at, value, version);
    if (!answer.done) {
      return failed(502, answer.refusal.message);
    }
    return { ok: true, result: null };
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
    const answer = await envvars.remove(dialled(connector), held, slug, at, version);
    if (!answer.done) {
      return failed(502, answer.refusal.message);
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
    const answer = await envvars.versions(dialled(connector), held, slug, at);
    if (!answer.done) {
      return failed(502, answer.refusal.message);
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
    const listed = await envvars.list(dialled(connector), other, slug);
    if (!listed.done) {
      return failed(502, listed.refusal.message);
    }
    const readable = listed.result.filter((value) => value.reference === undefined);
    const shown = await envvars.reveal(dialled(connector), other, slug, readable);
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
    const shown = await envvars.reveal(dialled(connector), other, slug, cells);
    if (!shown.done) {
      return failed(502, shown.refusal.message);
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
        const answer = await envvars.set(dialled(connector), held, slug, at, value, at.version);
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
