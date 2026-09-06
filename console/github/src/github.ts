import type { Probot, ProbotOctokit } from "probot";
import type { PreviewClient, PreviewDeployment } from "./preview";

export function repoParts(repo: string): { owner: string; name: string } {
  const [owner, name] = repo.split("/");
  if (!owner || !name) throw new Error(`invalid repo '${repo}'`);
  return { owner, name };
}

export async function clientFor(probot: Probot, repo: string): Promise<PreviewClient> {
  const { owner, name } = repoParts(repo);
  const app = await probot.auth();
  const { data: installation } = await app.rest.apps.getRepoInstallation({ owner, repo: name });
  const octokit = await probot.auth(installation.id);
  return previewClient(octokit, owner, name);
}

export function previewClient(octokit: ProbotOctokit, owner: string, repo: string): PreviewClient {
  return {
    async listDeployments({ environment, sha }) {
      const { data } = await octokit.rest.repos.listDeployments({
        owner,
        repo,
        environment,
        sha,
        per_page: 100,
      });
      return data.map((deployment) => ({
        id: deployment.id,
        environment: deployment.environment,
        sha: deployment.sha,
      }));
    },

    async createDeployment({ ref, environment, description }): Promise<PreviewDeployment> {
      const { data } = await octokit.rest.repos.createDeployment({
        owner,
        repo,
        ref,
        environment,
        description,
        transient_environment: true,
        auto_merge: false,
        required_contexts: [],
      });
      if (!("id" in data)) throw new Error(`github refused the deployment: ${data.message}`);
      return { id: data.id, environment: data.environment, sha: data.sha };
    },

    async createDeploymentStatus({ deploymentId, state, logUrl, environmentUrl, autoInactive }) {
      await octokit.rest.repos.createDeploymentStatus({
        owner,
        repo,
        deployment_id: deploymentId,
        state,
        log_url: logUrl,
        environment_url: environmentUrl,
        auto_inactive: autoInactive,
      });
    },

    async listComments({ pr }) {
      const { data } = await octokit.rest.issues.listComments({
        owner,
        repo,
        issue_number: pr,
        per_page: 100,
      });
      return data.map((comment) => ({
        id: comment.id,
        body: comment.body ?? "",
        fromApp: comment.user?.type === "Bot",
      }));
    },

    async createComment({ pr, body }) {
      await octokit.rest.issues.createComment({ owner, repo, issue_number: pr, body });
    },

    async updateComment({ commentId, body }) {
      await octokit.rest.issues.updateComment({ owner, repo, comment_id: commentId, body });
    },
  };
}
