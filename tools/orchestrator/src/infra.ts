// Per-run Docker network + Postgres sidecar that sandboxed agents join, so
// apps/web's Postgres-backed tests can run inside the sandbox instead of
// being skipped there. One sidecar per orchestrator run; one database per
// agent (see `databaseNameFor`) so parallel agents don't collide.

import { execFileSync, spawnSync } from "node:child_process";

function docker(args: string[]): string {
	const res = spawnSync("docker", args, { encoding: "utf8" });
	if (res.status !== 0) {
		throw new Error(`docker ${args.join(" ")} failed: ${res.stderr.trim()}`);
	}
	return res.stdout.trim();
}

export function databaseNameFor(issueId: string): string {
	return `ocel_agent_${issueId.toLowerCase().replace(/[^a-z0-9]+/g, "_")}`;
}

export interface RunInfra {
	networkName: string;
	postgresContainerName: string;
	testDatabaseUrlFor(issueId: string): string;
	teardown(): void;
}

export function setupRunInfra(runId: string): RunInfra {
	const networkName = `ocel-orchestrator-${runId}`;
	const postgresContainerName = `ocel-orchestrator-pg-${runId}`;

	docker(["network", "create", networkName]);
	docker([
		"run",
		"-d",
		"--name",
		postgresContainerName,
		"--network",
		networkName,
		"-e",
		"POSTGRES_USER=postgres",
		"-e",
		"POSTGRES_PASSWORD=postgres",
		"postgres:16",
	]);

	// Wait for Postgres to accept connections before agents start racing to
	// CREATE DATABASE against it.
	const deadline = Date.now() + 60_000;
	for (;;) {
		const res = spawnSync("docker", ["exec", postgresContainerName, "pg_isready", "-U", "postgres"], { encoding: "utf8" });
		if (res.status === 0) break;
		if (Date.now() > deadline) {
			throw new Error(`Postgres sidecar ${postgresContainerName} did not become ready within 60s`);
		}
		execFileSync("sleep", ["1"]);
	}

	return {
		networkName,
		postgresContainerName,
		testDatabaseUrlFor(issueId: string) {
			return `postgres://postgres:postgres@${postgresContainerName}:5432/${databaseNameFor(issueId)}`;
		},
		teardown() {
			spawnSync("docker", ["rm", "-f", postgresContainerName]);
			spawnSync("docker", ["network", "rm", networkName]);
		},
	};
}
