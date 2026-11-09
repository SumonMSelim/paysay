import type { PaypalClient } from "paypal";
import type { JobConfig } from "./config.js";
import { fetchInvoices, selectChaseCandidates } from "./invoices.js";
import { findPaidInvoiceIds, type Logger } from "./reconcile.js";
import { buildPlan, postPlan, type CollectionsPlan } from "./plan.js";
import { buildPayPalClient } from "./paypal.js";

export interface RunDeps {
  /** HTTP transport for the app calls. Defaults to global fetch. */
  fetchImpl?: typeof fetch;
  /** Pre-built PayPal client. Defaults to one built from cfg. */
  client?: PaypalClient;
  /** Clock, injectable for deterministic tests. */
  now?: Date;
  /** Logger for warnings. Defaults to console. */
  logger?: Logger;
}

/**
 * Runs the daily collections check:
 *   (a) fetch invoices from the app and pick sent-but-unpaid candidates,
 *   (b) reconcile against PayPal Transaction Search so paid customers are not
 *       chased,
 *   (c) post the plan to the app for merchant approval.
 *
 * The job never sends reminders itself; it only produces a plan the merchant
 * approves through the app's existing confirmation gate. Returns the plan that
 * was posted, so a caller (or a workflow run) can report counts.
 */
export async function runCollectionsCheck(cfg: JobConfig, deps: RunDeps = {}): Promise<CollectionsPlan> {
  const fetchImpl = deps.fetchImpl ?? fetch;
  const now = deps.now ?? new Date();
  const logger = deps.logger ?? console;
  const client = deps.client ?? buildPayPalClient(cfg);

  const invoices = await fetchInvoices(cfg, fetchImpl);
  const candidates = selectChaseCandidates(invoices, now);

  // Reconciliation is best-effort: if Transaction Search is unavailable, we
  // chase every candidate rather than skipping anyone.
  const paidInvoiceIds = candidates.length > 0
    ? await findPaidInvoiceIds(client, now, logger)
    : new Set<string>();

  const plan = buildPlan(candidates, paidInvoiceIds, now);
  await postPlan(cfg, plan, fetchImpl);
  return plan;
}
