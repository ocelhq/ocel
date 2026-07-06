#!/usr/bin/env node
// Selects unblocked issues from a `.scratch/<folder>/issues/` directory and
// implements them one at a time, each in its own git worktree, via the
// `claude` CLI. See `.scratch/<folder>/orchestrator-runs/<timestamp>/` for
// per-run logs after a run.

import { spawnSync } from "node:child_process";
import {
	appendFileSync,
	cpSync,
	existsSync,
	mkdirSync,
	readdirSync,
	readFileSync,
	writeFileSync,
} from "node:fs";
import path from "node:path";

const MAX_ITERATIONS = 3;
const IMPLEMENT_TIMEOUT_MS = 30 * 60 * 1000;
const WORKTREE_BASE_DIRNAME = "ocel-orchestrator-worktrees";

const SELECTION_SCHEMA = {
	type: "object",
	properties: {
		issues: {
			type: "array",
			items: {
				type: "object",
				properties: {
					id: { type: "string" },
					title: { type: "string" },
					branch: { type: "string" },
				},
				required: ["id", "title", "branch"],
			},
		},
	},
	required: ["issues"],
};

function usageError(message) {
	console.error(message);
	console.error("Usage: node scripts/orchestrator.mjs <folder-name>");
	process.exit(1);
}

function git(args, cwd) {
	const res = spawnSync("git", args, { cwd, encoding: "utf8" });
	if (res.status !== 0) {
		throw new Error(`git ${args.join(" ")} failed: ${res.stderr.trim()}`);
	}
	return res.stdout.trim();
}

function remoteBranchExists(branch, cwd) {
	const res = spawnSync("git", ["ls-remote", "--exit-code", "--heads", "origin", branch], {
		cwd,
		encoding: "utf8",
	});
	return res.status === 0;
}

function listMarkdownFiles(dir) {
	if (!existsSync(dir)) return [];
	return readdirSync(dir, { withFileTypes: true })
		.filter((e) => e.isFile() && e.name.endsWith(".md"))
		.map((e) => e.name)
		.sort();
}

function readIssueStatus(filePath) {
	const content = readFileSync(filePath, "utf8");
	const match = content.match(/^Status:\s*(.+)$/m);
	return { content, status: match ? match[1].trim() : null };
}

function setIssueStatus(filePath, newStatus) {
	const { content } = readIssueStatus(filePath);
	const updated = content.match(/^Status:\s*.+$/m)
		? content.replace(/^Status:\s*.+$/m, `Status: ${newStatus}`)
		: content;
	writeFileSync(filePath, updated);
}

function buildSelectionPrompt(issuesDir, openIssueNames, doneIssueNames, excludeIds) {
	const issueBlocks = openIssueNames
		.map((name) => `--- FILE: issues/${name} ---\n${readFileSync(path.join(issuesDir, name), "utf8")}`)
		.join("\n\n");

	return `# TASK

Analyze the open issues below and determine which are unblocked right now.

Each issue file has its own "## Blocked by" section listing the issues (by filename) it depends on. Treat that section as authoritative for *which* issues are dependencies. You may note (in prose, not in the JSON output) any additional file/module-overlap conflicts you notice between issues, as a warning only — do not treat those as hard blockers.

An issue is unblocked when every filename listed in its "## Blocked by" section appears in the COMPLETED ISSUES list below, or when it says "None". An open issue file still present in the OPEN ISSUES list is NOT complete, even if it looks nearly done.

Only consider issues whose "Status:" line is exactly "ready-for-agent". Skip anything with needs-triage, needs-info, ready-for-human, wontfix, or in-progress.

Do not include any of these issue ids — they failed earlier in this run and should be skipped: ${excludeIds.length ? excludeIds.join(", ") : "(none)"}

For each unblocked issue, assign a branch name using the format ocel/issue-{slug}, where {slug} is the issue's filename with the .md extension and any leading numeric prefix (e.g. "02-") stripped (e.g. issues/02-create-and-read-back-project.md -> ocel/issue-create-and-read-back-project).

## COMPLETED ISSUES (already in issues/done/)
${doneIssueNames.length ? doneIssueNames.join("\n") : "(none yet)"}

## OPEN ISSUES

${issueBlocks}

# OUTPUT

Return every currently unblocked issue, each with its filename stem (no .md extension) as "id". If every issue is blocked, return the single highest-priority candidate (fewest / weakest dependencies) as the only entry.`;
}

function buildImplementPrompt(issueRelPath, branch, baseBranch) {
	return `# TASK

Fix issue ${issueRelPath}

Pull in the issue from the path provided. If it has a parent PRD (see the "## Parent" section), pull that in too.

Only work on the issue specified.

You are already on branch ${branch} in a dedicated worktree. Make commits, run tests, and create a PR against ${baseBranch} (not main). Use the \`gh\` cli, which is installed, to do so.

Do not stage or commit any changes under \`.scratch/\` — that directory is local bookkeeping ferried in and out of this worktree by the orchestrator, not part of the code change.

# CONTEXT

# EXPLORATION

Explore the repo and fill your context window with relevant information that will allow you to complete the task.

Pay extra attention to test files that touch the relevant parts of the code.

# EXECUTION

If applicable, use RGR to complete the task.

1. RED: write one test
2. GREEN: write the implementation to pass that test
3. REPEAT until done
4. REFACTOR the code

# FEEDBACK LOOPS

Before committing, run the relevant typecheck and test commands for the part of the repo you touched, and ensure they pass.

# COMMIT

Make a git commit. The commit message must:

1. Have a concise descriptive title
2. Include task completed + PRD reference
3. Key decisions made
4. Files changed
5. Blockers or notes for next iteration

Keep it concise.

# THE ISSUE

If the task is complete, move the issue file to \`issues/done/\`.

If the task is not complete, add a note to the issue file with what was done.

# FINAL RULES

ONLY WORK ON A SINGLE TASK.`;
}

function runClaudeSelection(prompt, repoRoot) {
	const res = spawnSync(
		"claude",
		["-p", "--output-format", "json", "--json-schema", JSON.stringify(SELECTION_SCHEMA), "--permission-mode", "bypassPermissions"],
		{ cwd: repoRoot, input: prompt, encoding: "utf8", maxBuffer: 1024 * 1024 * 100 },
	);
	if (res.error) throw new Error(`selection call failed to spawn: ${res.error.message}`);
	if (res.status !== 0) throw new Error(`selection call exited ${res.status}: ${res.stderr}`);

	let envelope;
	try {
		envelope = JSON.parse(res.stdout);
	} catch {
		throw new Error(`selection call produced non-JSON output: ${res.stdout.slice(0, 2000)}`);
	}
	if (envelope.is_error || envelope.subtype !== "success") {
		throw new Error(`selection call returned an error result: ${res.stdout.slice(0, 2000)}`);
	}
	return { issues: envelope.structured_output?.issues ?? [], raw: res.stdout };
}

function runClaudeImplement(prompt, cwd) {
	const res = spawnSync("claude", ["-p", "--permission-mode", "bypassPermissions"], {
		cwd,
		input: prompt,
		encoding: "utf8",
		maxBuffer: 1024 * 1024 * 200,
		timeout: IMPLEMENT_TIMEOUT_MS,
		killSignal: "SIGKILL",
	});
	return res;
}

function main() {
	const folderName = process.argv[2];
	if (!folderName) usageError("Missing <folder-name> argument.");

	const repoRoot = git(["rev-parse", "--show-toplevel"], process.cwd());
	const scratchDir = path.join(repoRoot, ".scratch", folderName);
	const issuesDir = path.join(scratchDir, "issues");
	const doneDir = path.join(issuesDir, "done");

	if (!existsSync(issuesDir)) usageError(`No issues directory found at ${issuesDir}`);

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
	const runDir = path.join(scratchDir, "orchestrator-runs", runTimestamp);
	mkdirSync(runDir, { recursive: true });

	function log(msg) {
		const line = `[${new Date().toISOString()}] ${msg}`;
		console.log(line);
		appendFileSync(path.join(runDir, "run.log"), `${line}\n`);
	}

	const worktreeBase = path.resolve(repoRoot, "..", WORKTREE_BASE_DIRNAME);
	const failedIds = new Set();

	log(`Starting orchestrator run for folder "${folderName}" on base branch "${baseBranch}"`);

	for (let iteration = 1; iteration <= MAX_ITERATIONS; iteration++) {
		log(`--- Supercycle ${iteration}/${MAX_ITERATIONS} ---`);

		const openIssueNames = listMarkdownFiles(issuesDir);
		const doneIssueNames = listMarkdownFiles(doneDir);

		if (openIssueNames.length === 0) {
			log("No open issues remain. Stopping.");
			break;
		}

		const prompt = buildSelectionPrompt(issuesDir, openIssueNames, doneIssueNames, [...failedIds]);

		let selection;
		try {
			selection = runClaudeSelection(prompt, repoRoot);
		} catch (err) {
			log(`Selection call failed: ${err.message}`);
			break;
		}
		writeFileSync(path.join(runDir, `selection-${iteration}.json`), selection.raw);

		if (selection.issues.length === 0) {
			log("Selection returned no candidates. Stopping.");
			break;
		}

		const candidate = selection.issues[0];
		const issueFileName = `${candidate.id}.md`;
		const issueFilePath = path.join(issuesDir, issueFileName);

		if (!existsSync(issueFilePath)) {
			log(`Selected id "${candidate.id}" does not match an existing open issue file. Skipping.`);
			failedIds.add(candidate.id);
			continue;
		}

		log(`Selected "${candidate.id}" -> branch ${candidate.branch}`);

		const { status: originalStatus } = readIssueStatus(issueFilePath);
		setIssueStatus(issueFilePath, "in-progress");

		let worktreePath;
		try {
			worktreePath = path.join(worktreeBase, candidate.branch);
			mkdirSync(path.dirname(worktreePath), { recursive: true });
			git(["worktree", "add", "-b", candidate.branch, worktreePath, baseBranch], repoRoot);
		} catch (err) {
			log(`Failed to create worktree for "${candidate.branch}": ${err.message}`);
			setIssueStatus(issueFilePath, originalStatus ?? "ready-for-agent");
			failedIds.add(candidate.id);
			continue;
		}

		const worktreeScratchDir = path.join(worktreePath, ".scratch", folderName);
		cpSync(scratchDir, worktreeScratchDir, { recursive: true });

		const issueRelPath = path.relative(worktreePath, path.join(worktreeScratchDir, "issues", issueFileName));
		const implementPrompt = buildImplementPrompt(issueRelPath, candidate.branch, baseBranch);

		log(`Running implement call for "${candidate.id}" in ${worktreePath} (timeout ${IMPLEMENT_TIMEOUT_MS / 60000}min)`);
		const implementRes = runClaudeImplement(implementPrompt, worktreePath);
		writeFileSync(
			path.join(runDir, `implement-${candidate.id}.log`),
			`${implementRes.stdout ?? ""}\n--- stderr ---\n${implementRes.stderr ?? ""}`,
		);

		// Ferry the worktree's .scratch state back into the main checkout so the
		// next selection cycle sees whatever the sub-agent did (done-move, notes).
		cpSync(worktreeScratchDir, scratchDir, { recursive: true });

		const timedOut = implementRes.signal === "SIGKILL" || implementRes.error?.code === "ETIMEDOUT";
		const succeeded = !timedOut && !implementRes.error && implementRes.status === 0 && existsSync(path.join(doneDir, issueFileName));

		if (succeeded) {
			log(`Issue "${candidate.id}" completed successfully.`);
		} else {
			const reason = timedOut
				? "timed out"
				: implementRes.error
					? `spawn error: ${implementRes.error.message}`
					: implementRes.status !== 0
						? `exited with status ${implementRes.status}`
						: "did not move issue file to issues/done/";
			log(`Issue "${candidate.id}" did not complete (${reason}). Reverting status, worktree left at ${worktreePath} for inspection.`);
			if (existsSync(issueFilePath)) {
				setIssueStatus(issueFilePath, originalStatus ?? "ready-for-agent");
			}
			failedIds.add(candidate.id);
		}
	}

	log("Orchestrator run finished.");
}

main();
