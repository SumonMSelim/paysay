import { Render } from "@renderinc/sdk";

// The task name registered in workflow.ts. The full task slug is
// "{workflow-slug}/{taskName}".
const TASK_NAME = "dailyCollectionsCheck";

// Entry point for the Render Cron Job. Render Workflows does not yet support
// native scheduling, so a Cron Job triggers a workflow run on a schedule. This
// script triggers the dailyCollectionsCheck task through the Render SDK and
// waits for it to complete, exiting non-zero if the run fails.
//
// Required environment:
//   RENDER_API_KEY — used by the SDK client automatically.
//   WORKFLOW_SLUG  — the workflow service's slug; the Blueprint wires this from
//                    the workflow service's `slug` property.
async function main(): Promise<void> {
  const workflowSlug = process.env.WORKFLOW_SLUG;
  if (!workflowSlug || workflowSlug.trim() === "") {
    throw new Error("WORKFLOW_SLUG is not configured (the workflow service's slug).");
  }
  if (!process.env.RENDER_API_KEY || process.env.RENDER_API_KEY.trim() === "") {
    throw new Error("RENDER_API_KEY is not configured.");
  }

  const taskSlug = `${workflowSlug}/${TASK_NAME}`;

  // The client reads RENDER_API_KEY from the environment automatically.
  const render = new Render();

  // runTask starts the run and waits for it to finish. The task takes no
  // arguments, so the input array is empty.
  const run = await render.workflows.runTask(taskSlug, []);

  // Treat the run as failed only if it reached a failure/cancelled terminal
  // state. TaskRunStatus is
  //   "pending" | "running" | "completed" | "succeeded" | "failed" |
  //   "canceled" | "paused"
  // (node_modules/@renderinc/sdk/dist/generated/schema.d.ts, `TaskRunStatus`).
  const failureStates = new Set(["failed", "canceled"]);
  if (failureStates.has(run.status)) {
    throw new Error(`collections workflow run ${run.id} finished with status "${run.status}"`);
  }
  console.log(`collections workflow run ${run.id} finished with status "${run.status}"`);
}

main().catch((err: unknown) => {
  console.error(err instanceof Error ? err.message : "collections trigger failed");
  process.exitCode = 1;
});
