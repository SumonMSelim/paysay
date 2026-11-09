import type { PaypalClient } from "paypal";
import { ApiError, PaypalError } from "paypal";

// The maximum date range the Transaction Search API supports is 31 days
// (verified in the SDK's api-reference.md for searchTransactions). We look back
// across the whole window so a recent payment on an older invoice is seen.
export const SEARCH_WINDOW_DAYS = 31;

// A PayPal transaction status of "S" means the transaction completed
// successfully, without a denial and after any pending statuses (verified in
// the searchTransactions parameter docs). Only a completed payment counts as
// "already paid" for reconciliation.
const COMPLETED_STATUS = "S";

const MS_PER_DAY = 24 * 60 * 60 * 1000;

export interface Logger {
  warn(message: string): void;
}

/**
 * Queries PayPal Transaction Search over the last 31 days and returns the set
 * of values found in completed transactions' transaction_info.invoice_id field.
 *
 * For PayPal Invoicing, that field carries the merchant's invoice NUMBER (e.g.
 * "0007"), not the resource ID (INV2-...). The caller therefore matches it
 * against each invoice's number first, falling back to its ID. (Confirmed
 * against the live sandbox: a paid invoice with number "0001" appeared in
 * Transaction Search with transaction_info.invoice_id "0001" and
 * transaction_status "S".)
 *
 * If Transaction Search fails for any reason — most commonly a 403 because the
 * feature is not enabled on the PayPal app — this returns an empty set and logs
 * a warning, so the job still posts a plan (with no skips) rather than crashing.
 * It never logs request or response bodies.
 */
export async function findPaidInvoiceIds(
  client: PaypalClient,
  now: Date = new Date(),
  logger: Logger = console,
): Promise<Set<string>> {
  const paid = new Set<string>();
  try {
    const endDate = now;
    const startDate = new Date(now.getTime() - SEARCH_WINDOW_DAYS * MS_PER_DAY);

    let page = 1;
    // Transaction Search paginates; walk every page so a payment on any page is
    // seen. page_size defaults to 100; we read total_pages from the response.
    for (;;) {
      const response = await client.transactionSearch.searchTransactions({
        startDate: toSearchDate(startDate),
        endDate: toSearchDate(endDate),
        page,
      });

      for (const detail of response.transactionDetails ?? []) {
        const info = detail.transactionInfo;
        if (!info) continue;
        if (info.transactionStatus !== COMPLETED_STATUS) continue;
        // invoice_id here is the merchant's invoice number for Invoicing
        // payments; the caller matches it against number, then id.
        const invoiceRef = info.invoiceId;
        if (invoiceRef) paid.add(invoiceRef);
      }

      const totalPages = response.totalPages ?? 1;
      if (page >= totalPages) break;
      page += 1;
    }
  } catch (err) {
    // Degrade gracefully: no reconciliation data means no skips, not a crash.
    // Log only the error kind and HTTP status, never a message, URL, query, or
    // response body (an ApiError's message embeds the request URL and status).
    logger.warn(`transaction search unavailable, continuing with no skips: ${describeError(err)}`);
    return new Set<string>();
  }
  return paid;
}

// toSearchDate formats a date as the RFC 3339 string Transaction Search
// requires (seconds are required; fractional seconds are optional).
function toSearchDate(d: Date): string {
  return d.toISOString();
}

// describeError returns a short description safe to log: the SDK error kind and,
// for an API error, the HTTP status. It never includes the error message, which
// embeds the request URL and query string.
function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    return `api error (status ${err.status})`;
  }
  if (err instanceof PaypalError) {
    // The family's kind ("auth", "connection", "timeout", "decode", "encode").
    return `${err.kind} error`;
  }
  return "unknown error";
}
