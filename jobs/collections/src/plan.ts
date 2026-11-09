import type { JobConfig } from "./config.js";
import type { InvoiceRow } from "./invoices.js";

// The plan posted to the app's internal collections endpoint. Field names are
// snake_case to match the Go handler (internal/server/collections.go).
export interface PlanInvoice {
  id: string;
  customer: string;
  amount: string;
  currency: string;
  reason: string;
}

export interface CollectionsPlan {
  generated_at: string;
  chase: PlanInvoice[];
  skipped: PlanInvoice[];
}

/**
 * Builds the plan from the chase candidates and the set of invoice references
 * (as PayPal's Transaction Search reports them) found already paid. A candidate
 * whose invoice number — or, failing that, its resource ID — matches a
 * completed payment moves to "skipped: payment found"; the rest are chased.
 */
export function buildPlan(candidates: InvoiceRow[], paidRefs: Set<string>, now: Date = new Date()): CollectionsPlan {
  const chase: PlanInvoice[] = [];
  const skipped: PlanInvoice[] = [];

  for (const inv of candidates) {
    if (isPaid(inv, paidRefs)) {
      skipped.push(toPlanInvoice(inv, "payment found"));
    } else {
      chase.push(toPlanInvoice(inv, chaseReason(inv, now)));
    }
  }

  return {
    generated_at: now.toISOString(),
    chase,
    skipped,
  };
}

// isPaid reports whether a completed payment references this invoice. For
// PayPal Invoicing the transaction's invoice_id holds the merchant's invoice
// number, so match that first, then fall back to the resource ID.
function isPaid(inv: InvoiceRow, paidRefs: Set<string>): boolean {
  if (inv.number && paidRefs.has(inv.number)) return true;
  return paidRefs.has(inv.id);
}

function toPlanInvoice(inv: InvoiceRow, reason: string): PlanInvoice {
  return {
    id: inv.id,
    customer: inv.customer,
    amount: inv.amount,
    currency: inv.currency,
    reason,
  };
}

// chaseReason describes why an invoice is being chased, in plain language the
// merchant sees on the Overview card.
function chaseReason(inv: InvoiceRow, now: Date): string {
  const created = Date.parse(inv.created);
  if (Number.isNaN(created)) return "sent, unpaid";
  const days = Math.floor((now.getTime() - created) / (24 * 60 * 60 * 1000));
  return `sent ${days} days ago, unpaid`;
}

/**
 * Posts the plan to the app's internal collections endpoint with the bearer
 * token. Throws on a non-2xx response. Never logs the token or the body.
 */
export async function postPlan(cfg: JobConfig, plan: CollectionsPlan, fetchImpl: typeof fetch = fetch): Promise<void> {
  const res = await fetchImpl(`${cfg.appUrl}/api/internal/collections`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${cfg.collectionsToken}`,
    },
    body: JSON.stringify(plan),
  });
  if (res.status !== 204) {
    throw new Error(`POST /api/internal/collections failed with ${res.status}`);
  }
}
