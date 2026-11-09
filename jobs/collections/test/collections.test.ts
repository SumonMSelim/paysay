import { describe, it, expect } from "vitest";
import { PaypalClient } from "paypal";
import type { JobConfig } from "../src/config.js";
import { selectChaseCandidates, fetchInvoices, type InvoiceRow } from "../src/invoices.js";
import { findPaidInvoiceIds } from "../src/reconcile.js";
import { buildPlan, postPlan, type CollectionsPlan } from "../src/plan.js";
import { runCollectionsCheck } from "../src/collections.js";

const NOW = new Date("2026-11-07T09:00:00.000Z");

const cfg: JobConfig = {
  appUrl: "https://app.example",
  collectionsToken: "test-token",
  paypalClientId: "client-id",
  paypalClientSecret: "client-secret",
  paypalEnvironment: "SANDBOX",
};

// daysAgo returns an ISO timestamp n days before NOW.
function daysAgo(n: number): string {
  return new Date(NOW.getTime() - n * 24 * 60 * 60 * 1000).toISOString();
}

function invoice(partial: Partial<InvoiceRow> & { id: string }): InvoiceRow {
  return {
    number: "",
    status: "SENT",
    customer: "c@example.com",
    amount: "10.00",
    currency: "USD",
    created: daysAgo(10),
    ...partial,
  };
}

// --- SDK stubbing via ClientOptions.fetch (per the testing guide) ---

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status });
}

function tokenResponse(): Response {
  return jsonResponse(200, { access_token: "T", token_type: "Bearer", expires_in: 3600 });
}

// transactionsResponse builds a Transaction Search wire body with the given
// (invoice_id, transaction_status) pairs.
function transactionsResponse(rows: Array<{ invoiceId?: string; status: string }>): Response {
  return jsonResponse(200, {
    transaction_details: rows.map((r) => ({
      transaction_info: {
        transaction_status: r.status,
        ...(r.invoiceId ? { invoice_id: r.invoiceId } : {}),
      },
    })),
    total_pages: 1,
    page: 1,
  });
}

// clientWith routes the token request and the search request to the given
// responses, built over a stub fetch so no network call happens.
function clientWith(searchResponder: () => Response): PaypalClient {
  const stubFetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init);
    const url = new URL(request.url);
    if (url.pathname.endsWith("/v1/oauth2/token")) return tokenResponse();
    if (url.pathname.endsWith("/v1/reporting/transactions")) return searchResponder();
    throw new Error(`unexpected request to ${url.pathname}`);
  };
  return new PaypalClient({
    oauth2: { clientId: cfg.paypalClientId, clientSecret: cfg.paypalClientSecret },
    retry: { maxRetries: 0 },
    fetch: stubFetch,
  });
}

const silentLogger = { warn() {} };

describe("selectChaseCandidates", () => {
  it("keeps sent-but-unpaid invoices older than the minimum age", () => {
    const invoices = [
      invoice({ id: "INV2-OLD-SENT", status: "SENT", created: daysAgo(10) }),
      invoice({ id: "INV2-OLD-UNPAID", status: "UNPAID", created: daysAgo(5) }),
      invoice({ id: "INV2-OLD-PARTIAL", status: "PARTIALLY_PAID", created: daysAgo(4) }),
    ];
    const got = selectChaseCandidates(invoices, NOW).map((i) => i.id);
    expect(got).toEqual(["INV2-OLD-SENT", "INV2-OLD-UNPAID", "INV2-OLD-PARTIAL"]);
  });

  it("excludes drafts, paid, too-recent, and undated invoices", () => {
    const invoices = [
      invoice({ id: "INV2-DRAFT", status: "DRAFT", created: daysAgo(10) }),
      invoice({ id: "INV2-PAID", status: "PAID", created: daysAgo(10) }),
      invoice({ id: "INV2-RECENT", status: "SENT", created: daysAgo(1) }),
      invoice({ id: "INV2-NODATE", status: "SENT", created: "" }),
    ];
    expect(selectChaseCandidates(invoices, NOW)).toHaveLength(0);
  });
});

describe("findPaidInvoiceIds", () => {
  it("collects invoice ids with a completed payment", async () => {
    const client = clientWith(() =>
      transactionsResponse([
        { invoiceId: "INV2-PAID-1", status: "S" },
        { invoiceId: "INV2-PENDING", status: "P" }, // pending, not counted
        { invoiceId: "INV2-PAID-2", status: "S" },
        { status: "S" }, // no invoice id, ignored
      ]),
    );
    const paid = await findPaidInvoiceIds(client, NOW, silentLogger);
    expect([...paid].sort()).toEqual(["INV2-PAID-1", "INV2-PAID-2"]);
  });

  it("returns an empty set when no completed payment matches", async () => {
    const client = clientWith(() =>
      transactionsResponse([
        { invoiceId: "INV2-PENDING", status: "P" },
        { invoiceId: "INV2-DENIED", status: "D" },
      ]),
    );
    const paid = await findPaidInvoiceIds(client, NOW, silentLogger);
    expect(paid.size).toBe(0);
  });

  it("degrades to an empty set and warns when Transaction Search fails (e.g. 403)", async () => {
    let warned = "";
    const client = clientWith(() => jsonResponse(403, { name: "NOT_AUTHORIZED", message: "feature not enabled" }));
    const paid = await findPaidInvoiceIds(client, NOW, { warn: (m) => (warned = m) });
    expect(paid.size).toBe(0);
    expect(warned).toContain("transaction search unavailable");
  });
});

describe("buildPlan", () => {
  it("moves paid candidates to skipped and chases the rest", () => {
    const candidates = [
      invoice({ id: "INV2-CHASE-1", created: daysAgo(10) }),
      invoice({ id: "INV2-ALREADY-PAID", created: daysAgo(8) }),
    ];
    const plan = buildPlan(candidates, new Set(["INV2-ALREADY-PAID"]), NOW);
    expect(plan.chase.map((i) => i.id)).toEqual(["INV2-CHASE-1"]);
    expect(plan.skipped.map((i) => i.id)).toEqual(["INV2-ALREADY-PAID"]);
    expect(plan.skipped[0]?.reason).toBe("payment found");
    expect(plan.generated_at).toBe(NOW.toISOString());
  });

  it("matches a completed payment by invoice NUMBER, not resource ID", () => {
    // Transaction Search reports the invoice number ("0007") for Invoicing
    // payments, not the INV2- resource ID.
    const candidates = [invoice({ id: "INV2-RESOURCE-ID", number: "0007" })];
    const plan = buildPlan(candidates, new Set(["0007"]), NOW);
    expect(plan.chase).toHaveLength(0);
    expect(plan.skipped.map((i) => i.id)).toEqual(["INV2-RESOURCE-ID"]);
  });

  it("falls back to matching on the resource ID when the invoice has no number", () => {
    const candidates = [invoice({ id: "INV2-RESOURCE-ID", number: "" })];
    const plan = buildPlan(candidates, new Set(["INV2-RESOURCE-ID"]), NOW);
    expect(plan.skipped.map((i) => i.id)).toEqual(["INV2-RESOURCE-ID"]);
  });

  it("does not skip when neither number nor id matches", () => {
    const candidates = [invoice({ id: "INV2-RESOURCE-ID", number: "0007" })];
    const plan = buildPlan(candidates, new Set(["9999"]), NOW);
    expect(plan.chase.map((i) => i.id)).toEqual(["INV2-RESOURCE-ID"]);
    expect(plan.skipped).toHaveLength(0);
  });
});

describe("postPlan", () => {
  it("posts the plan with the bearer token and expects 204", async () => {
    let seen: { url: string; auth: string | null; body: unknown } | undefined;
    const fetchImpl: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      seen = {
        url: request.url,
        auth: request.headers.get("authorization"),
        body: JSON.parse(await request.clone().text()),
      };
      return new Response(null, { status: 204 });
    };
    const plan: CollectionsPlan = { generated_at: NOW.toISOString(), chase: [], skipped: [] };
    await postPlan(cfg, plan, fetchImpl);
    expect(seen?.url).toBe("https://app.example/api/internal/collections");
    expect(seen?.auth).toBe("Bearer test-token");
    expect(seen?.body).toEqual(plan);
  });

  it("throws on a non-204 response", async () => {
    const fetchImpl: typeof fetch = async () => new Response(null, { status: 401 });
    const plan: CollectionsPlan = { generated_at: NOW.toISOString(), chase: [], skipped: [] };
    await expect(postPlan(cfg, plan, fetchImpl)).rejects.toThrow(/401/);
  });
});

describe("runCollectionsCheck", () => {
  it("fetches, reconciles, and posts a plan end to end", async () => {
    let posted: CollectionsPlan | undefined;

    // One app GET (invoices) and one app POST (plan); the PayPal client is
    // stubbed separately and injected.
    const appFetch: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.pathname === "/api/invoices") {
        return jsonResponse(200, {
          invoices: [
            invoice({ id: "INV2-CHASE-1", number: "0006", created: daysAgo(10) }),
            invoice({ id: "INV2-ALREADY-PAID", number: "0007", created: daysAgo(8) }),
            invoice({ id: "INV2-DRAFT", status: "DRAFT", created: daysAgo(10) }),
          ],
        });
      }
      if (url.pathname === "/api/internal/collections") {
        posted = JSON.parse(await request.clone().text());
        return new Response(null, { status: 204 });
      }
      throw new Error(`unexpected app request to ${url.pathname}`);
    };

    // The completed transaction references the invoice NUMBER ("0007"), the way
    // PayPal Invoicing reports it, not the INV2- resource ID.
    const client = clientWith(() => transactionsResponse([{ invoiceId: "0007", status: "S" }]));

    const plan = await runCollectionsCheck(cfg, { fetchImpl: appFetch, client, now: NOW, logger: silentLogger });

    expect(plan.chase.map((i) => i.id)).toEqual(["INV2-CHASE-1"]);
    expect(plan.skipped.map((i) => i.id)).toEqual(["INV2-ALREADY-PAID"]);
    expect(posted).toEqual(plan);
  });

  it("posts a plan with no skips when there are no candidates (search not called)", async () => {
    let posted: CollectionsPlan | undefined;
    const appFetch: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.pathname === "/api/invoices") {
        return jsonResponse(200, { invoices: [invoice({ id: "INV2-DRAFT", status: "DRAFT" })] });
      }
      if (url.pathname === "/api/internal/collections") {
        posted = JSON.parse(await request.clone().text());
        return new Response(null, { status: 204 });
      }
      throw new Error(`unexpected app request to ${url.pathname}`);
    };
    // A PayPal client that fails if called, proving search is skipped.
    const client = clientWith(() => {
      throw new Error("searchTransactions should not be called with no candidates");
    });

    const plan = await runCollectionsCheck(cfg, { fetchImpl: appFetch, client, now: NOW, logger: silentLogger });
    expect(plan.chase).toHaveLength(0);
    expect(plan.skipped).toHaveLength(0);
    expect(posted).toEqual(plan);
  });

  it("still posts a plan (no skips) when Transaction Search fails", async () => {
    let posted: CollectionsPlan | undefined;
    const appFetch: typeof fetch = async (input, init) => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.pathname === "/api/invoices") {
        return jsonResponse(200, { invoices: [invoice({ id: "INV2-CHASE-1", created: daysAgo(10) })] });
      }
      if (url.pathname === "/api/internal/collections") {
        posted = JSON.parse(await request.clone().text());
        return new Response(null, { status: 204 });
      }
      throw new Error(`unexpected app request to ${url.pathname}`);
    };
    const client = clientWith(() => jsonResponse(403, { message: "feature not enabled" }));

    const plan = await runCollectionsCheck(cfg, { fetchImpl: appFetch, client, now: NOW, logger: silentLogger });
    expect(plan.chase.map((i) => i.id)).toEqual(["INV2-CHASE-1"]);
    expect(plan.skipped).toHaveLength(0);
    expect(posted?.chase).toHaveLength(1);
  });
});

describe("app request timeouts", () => {
  // The real timeout is 30s; the unit tests assert the wiring (an AbortSignal
  // is passed) and that an abort propagates as a rejection, without waiting for
  // the full duration. A fetch stub that rejects as soon as the signal aborts,
  // combined with a short manual abort, exercises the same code path.
  function abortableFetch(): { impl: typeof fetch; sawSignal: () => boolean; abort: () => void } {
    const controller = new AbortController();
    let sawSignal = false;
    const impl: typeof fetch = async (_input, init) => {
      sawSignal = init?.signal instanceof AbortSignal;
      return await new Promise<Response>((_resolve, reject) => {
        // Reject when either the request's own timeout signal or our manual
        // controller aborts.
        init?.signal?.addEventListener("abort", () => reject(new Error("The operation was aborted")));
        controller.signal.addEventListener("abort", () => reject(new Error("The operation was aborted")));
      });
    };
    return { impl, sawSignal: () => sawSignal, abort: () => controller.abort() };
  }

  it("fetchInvoices passes an abort signal and rejects when it aborts", async () => {
    const f = abortableFetch();
    const p = fetchInvoices(cfg, f.impl);
    const assertion = expect(p).rejects.toThrow(/abort/i);
    f.abort();
    await assertion;
    expect(f.sawSignal()).toBe(true);
  });

  it("postPlan passes an abort signal and rejects when it aborts", async () => {
    const f = abortableFetch();
    const plan: CollectionsPlan = { generated_at: NOW.toISOString(), chase: [], skipped: [] };
    const p = postPlan(cfg, plan, f.impl);
    const assertion = expect(p).rejects.toThrow(/abort/i);
    f.abort();
    await assertion;
    expect(f.sawSignal()).toBe(true);
  });
});
