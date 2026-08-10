import { spawnSync } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { claudeCode, run as sandcastleRun } from "@ai-hero/sandcastle";
import { docker } from "@ai-hero/sandcastle/sandboxes/docker";
import { bdWhere, claimBatch, issueBlockers, issueStatus, revertClaim } from "./bd.ts";
import { branchNameFor, git, isMergedInto, localBranchExists, remoteBranchExists } from "./git.ts";
import { gtSync, gtTrack } from "./gt.ts";
import type { RunInfra } from "./infra.ts";
import { setupRunInfra } from "./infra.ts";
import { submitWaveWithModel, type WaveOutcome } from "./submit.ts";

const TRUNK_BRANCH = "main";

const MAX_PARALLEL_ISSUES = 3;
const MAX_CLAIM_WAVES = 20;
const IDLE_TIMEOUT_SECONDS = 30 * 60;
const IMAGE_NAME = "sandcastle:ocelhq";

function usageError(message: string): never {
	console.error(message);
	console.error("Usage: pnpm --filter @ocel/orchestrator orchestrate <parent-issue-id>");
	process.exit(1);
}

function resolveParentBranch(issue: { id: string }, baseBranch: string, repoRoot: string): string {
	const blockers = issueBlockers(issue.id, repoRoot);
	const candidates = blockers
		.map((blocker) => branchNameFor(blocker))
		.filter((branch) => localBranchExists(branch, repoRoot) && !isMergedInto(branch, baseBranch, repoRoot));

	if (candidates.length <= 1) return candidates[0] ?? baseBranch;

	const deepest = candidates.find((candidate) =>
		candidates.every((other) => other === candidate || isMergedInto(other, candidate, repoRoot)),
	);
	return deepest ?? candidates[candidates.length - 1] ?? baseBranch;
}

function ensureImageBuilt(repoRoot: string, sandcastleBin: string, log: (msg: string) => void) {
	const inspect = spawnSync("docker", ["image", "inspect", IMAGE_NAME], { encoding: "utf8" });
	if (inspect.status === 0) return;
	log(`Image ${IMAGE_NAME} not found — building from .sandcastle/Dockerfile...`);
	const build = spawnSync(sandcastleBin, ["docker", "build-image", "--image-name", IMAGE_NAME], {
		cwd: repoRoot,
		encoding: "utf8",
		stdio: "inherit",
	});
	if (build.status !== 0) {
		throw new Error(`sandcastle docker build-image failed with status ${build.status}`);
	}
}

async function main() {
	let infra: RunInfra | undefined;
	for (const signal of ["SIGINT", "SIGTERM"] as const) {
		process.on(signal, () => {
			console.error(`Received ${signal}, tearing down run infra...`);
			infra?.teardown();
			process.exit(1);
		});
	}

	const parentId = process.argv[2];
	if (!parentId) usageError("Missing <parent-issue-id> argument.");

	const repoRoot = git(["rev-parse", "--show-toplevel"], process.cwd());
	const sandcastleBin = path.join(repoRoot, "tools", "orchestrator", "node_modules", ".bin", "sandcastle");

	const parentCheck = spawnSync("bd", ["show", parentId, "--json"], { cwd: repoRoot, encoding: "utf8" });
	if (parentCheck.status !== 0) {
		usageError(`No bd issue found with id "${parentId}". Run 'bd list --type=epic' to find the right id.`);
	}

	const gitStatus = git(["status", "--porcelain"], repoRoot);
	if (gitStatus) {
		console.error("Working tree is not clean. Commit or stash changes before running the orchestrator.");
		process.exit(1);
	}

	const baseBranch = git(["rev-parse", "--abbrev-ref", "HEAD"], repoRoot);
	if (!remoteBranchExists(baseBranch, repoRoot)) {
		console.log(`Pushing base branch ${baseBranch} to origin...`);
		git(["push", "-u", "origin", baseBranch], repoRoot);
	}

	const runTimestamp = new Date().toISOString().replace(/[:.]/g, "-");
	const runId = runTimestamp.toLowerCase();
	const runDir = path.join(repoRoot, ".scratch", parentId, "orchestrator-runs", runTimestamp);
	mkdirSync(runDir, { recursive: true });

	function log(msg: string) {
		const line = `[${new Date().toISOString()}] ${msg}`;
		console.log(line);
		appendFileSync(path.join(runDir, "run.log"), `${line}\n`);
	}

	log(`Starting orchestrator run for parent "${parentId}" on base branch "${baseBranch}"`);

	ensureImageBuilt(repoRoot, sandcastleBin, log);

	infra = setupRunInfra(runId);
	log(`Run infra ready: network ${infra.networkName}, Postgres sidecar ${infra.postgresContainerName}`);

	const beadsDir = bdWhere(repoRoot);
	const beadsMount = { hostPath: beadsDir, sandboxPath: beadsDir };

	gtTrack(baseBranch, TRUNK_BRANCH, repoRoot);
	log(`Tracked feature branch "${baseBranch}" with Graphite (parent: ${TRUNK_BRANCH})`);

	try {
		for (let wave = 1; wave <= MAX_CLAIM_WAVES; wave++) {
			log(`--- Wave ${wave}/${MAX_CLAIM_WAVES} ---`);

			try {
				gtSync(repoRoot);
			} catch (err) {
				log(`gt sync failed: ${(err as Error).message}. Stopping for manual resolution.`);
				break;
			}

			let batch: ReturnType<typeof claimBatch>;
			try {
				batch = claimBatch(parentId, repoRoot, MAX_PARALLEL_ISSUES);
			} catch (err) {
				log(`Claiming ready issues failed: ${(err as Error).message}`);
				break;
			}

			if (batch.length === 0) {
				log("No ready issues remain under this parent. Stopping.");
				break;
			}

			log(`Claimed ${batch.length} issue(s) to run in parallel this wave: ${batch.map((c) => c.id).join(", ")}`);

			const settled = await Promise.allSettled(
				batch.map(async (issue) => {
					const branch = branchNameFor(issue);
					const parentBranch = resolveParentBranch(issue, baseBranch, repoRoot);
					const logFilePath = path.join(runDir, `implement-${issue.id}.log`);
					log(`[${issue.id}] Starting sandboxed implement run on branch ${branch} (stacked on ${parentBranch})`);

					let result: Awaited<ReturnType<typeof sandcastleRun>>;
					try {
						result = await sandcastleRun({
							cwd: repoRoot,
							name: issue.id,
							agent: claudeCode("claude-sonnet-5", { captureSessions: false }),
							sandbox: docker({
								imageName: IMAGE_NAME,
								network: infra.networkName,
								mounts: [beadsMount],
								env: { BEADS_DIR: beadsMount.sandboxPath, TEST_DATABASE_URL: infra.testDatabaseUrlFor(issue.id) },
							}),
							branchStrategy: { type: "branch", branch, baseBranch: parentBranch },
							promptFile: path.join(repoRoot, ".sandcastle", "implement-prompt.md"),

							promptArgs: { ISSUE_ID: issue.id, PARENT_ID: parentId },

							maxIterations: 1,
							idleTimeoutSeconds: IDLE_TIMEOUT_SECONDS,
							logging: {
								type: "file",
								path: logFilePath,
								onAgentStreamEvent: (event) => {
									if (event.type === "text") log(`[${issue.id}] assistant: ${event.message.slice(0, 300)}`);
									else if (event.type === "toolCall") log(`[${issue.id}] tool: ${event.name} (${event.formattedArgs.slice(0, 100)})`);
								},
							},
						});
					} catch (err) {
						log(`[${issue.id}] sandbox run threw: ${(err as Error).message}. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}

					const closed = issueStatus(issue.id, repoRoot) === "closed";
					if (!closed) {
						log(`[${issue.id}] did not close the bd issue. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}
					if (result.commits.length === 0) {
						log(`[${issue.id}] closed the issue but made no commits. Reverting claim.`);
						revertClaim(issue.id, repoRoot, log);
						return null;
					}

					log(`[${issue.id}] completed with ${result.commits.length} commit(s).`);
					return { issue, branch, parentBranch };
				}),
			);

			const waveOutcomes: WaveOutcome[] = [];
			for (const outcome of settled) {
				if (outcome.status === "fulfilled" && outcome.value !== null) waveOutcomes.push(outcome.value);
			}
			await submitWaveWithModel(waveOutcomes, repoRoot, runDir, wave, log);
		}
	} finally {
		infra.teardown();
		log(`Run infra torn down: ${infra.networkName}`);
	}

	log("Orchestrator run finished.");
}

main().catch((err) => {
	console.error(err);
	process.exit(1);
});
