import type { JobConfig } from "./config.js";

// An invoice row as the PaySay app returns it from GET /api/invoices. The
// shape is the app's normalized InvoiceRow (see internal/server/invoices.go):
// stable across the mock and live PayPal responses.
export interface InvoiceRow {
  id: string;
  number: string;
  status: string;
  customer: string;
  amount: string;
  currency: string;
  created: string;
}

// Statuses that mean an invoice was sent but is not yet fully paid, and so is a
// candidate for chasing. DRAFT is deliberately excluded: a draft was never
// sent, so there is no one to remind. These match the statuses the app's UI
// treats as "awaiting payment".
export const CHASE_STATUSES: ReadonlySet<string> = new Set(["SENT", "UNPAID", "PARTIALLY_PAID"]);

// Only chase invoices older than this. A freshly sent invoice does not need a
// reminder; three days gives the customer time to pay first.
export const MIN_AGE_DAYS = 3;

const MS_PER_DAY = 24 * 60 * 60 * 1000;

// APP_REQUEST_TIMEOUT_MS bounds each call to the PaySay app so a hung endpoint
// cannot hold the scheduled job open indefinitely.
export const APP_REQUEST_TIMEOUT_MS = 30_000;

/**
 * Fetches the merchant's invoices from the PaySay app. Throws on a non-2xx
 * response, a malformed body, or if the request exceeds the timeout.
 */
export async function fetchInvoices(cfg: JobConfig, fetchImpl: typeof fetch = fetch): Promise<InvoiceRow[]> {
  const res = await fetchImpl(`${cfg.appUrl}/api/invoices`, {
    signal: AbortSignal.timeout(APP_REQUEST_TIMEOUT_MS),
  });
  if (!res.ok) {
    throw new Error(`GET /api/invoices failed with ${res.status}`);
  }
  const body = (await res.json()) as { invoices?: InvoiceRow[] };
  return body.invoices ?? [];
}

/**
 * Selects the invoices worth chasing: sent-but-unpaid and older than
 * MIN_AGE_DAYS. `now` is injectable for deterministic tests.
 */
export function selectChaseCandidates(invoices: InvoiceRow[], now: Date = new Date()): InvoiceRow[] {
  const cutoff = now.getTime() - MIN_AGE_DAYS * MS_PER_DAY;
  return invoices.filter((inv) => {
    const status = String(inv.status || "").toUpperCase();
    if (!CHASE_STATUSES.has(status)) return false;
    const created = Date.parse(inv.created);
    if (Number.isNaN(created)) return false;
    return created < cutoff;
  });
}
