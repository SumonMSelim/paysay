import { PaypalClient } from "paypal";
import type { ClientOptions } from "paypal";
import type { JobConfig } from "./config.js";

// Production base URL. The SDK declares a single server group whose default is
// the sandbox host (https://api-m.sandbox.paypal.com), so reaching production
// is an explicit serverOptions.baseUrl override — verified against the SDK's
// src/servers.ts and src/client-options.ts.
const PRODUCTION_BASE_URL = "https://api-m.paypal.com";

/**
 * Builds a PaypalClient for the job. Credentials come from the client-
 * credentials OAuth2 grant; the SDK fetches and caches the token itself. The
 * client is long-lived (build once, reuse) so the token cache is shared.
 *
 * `fetchImpl` is injectable only so tests can stub the transport via
 * ClientOptions.fetch; production passes nothing and the SDK uses global fetch.
 */
export function buildPayPalClient(cfg: JobConfig, fetchImpl?: typeof fetch): PaypalClient {
  const options: ClientOptions = {
    oauth2: {
      clientId: cfg.paypalClientId,
      clientSecret: cfg.paypalClientSecret,
    },
    // A per-attempt budget so a hung PayPal endpoint cannot hold the job open
    // for the full default minute on every attempt.
    retry: { timeout: 15_000 },
    ...(cfg.paypalEnvironment === "PRODUCTION"
      ? { serverOptions: { baseUrl: PRODUCTION_BASE_URL } }
      : {}),
    ...(fetchImpl ? { fetch: fetchImpl } : {}),
  };
  return new PaypalClient(options);
}
