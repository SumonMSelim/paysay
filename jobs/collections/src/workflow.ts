import { task, type TaskContext } from "@renderinc/sdk/workflows";
import { loadConfig } from "./config.js";
import { runCollectionsCheck } from "./collections.js";

// Summary of one collections run, returned by the task so the Render Dashboard
// (and any caller that awaits the run) can see what happened. Must be
// JSON-serializable.
interface RunSummary {
  generatedAt: string;
  chased: number;
  skipped: number;
}

// The daily collections task. Render provides the TaskContext; the task takes
// no further arguments because everything it needs comes from the environment.
// It fetches candidates, reconciles against Transaction Search, and posts the
// plan to the app — it never sends reminders itself.
export const dailyCollectionsCheck = task(
  { name: "dailyCollectionsCheck" },
  async function dailyCollectionsCheck(_ctx: TaskContext): Promise<RunSummary> {
    const cfg = loadConfig();
    const plan = await runCollectionsCheck(cfg);
    return {
      generatedAt: plan.generated_at,
      chased: plan.chase.length,
      skipped: plan.skipped.length,
    };
  },
);
