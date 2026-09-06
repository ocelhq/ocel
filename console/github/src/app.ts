import type { ApplicationFunction } from "probot";
import { clientFor } from "./github";
import { reconcile } from "./preview";

export const app: ApplicationFunction = (probot) => {
  probot.on("pull_request.closed", async (context) => {
    const { pull_request: pull, repository } = context.payload;
    const client = await clientFor(probot, repository.full_name);
    await reconcile(client, {
      repo: repository.full_name,
      pr: pull.number,
      sha: pull.head.sha,
      ref: pull.head.ref,
      phase: "removed",
    });
  });
};
