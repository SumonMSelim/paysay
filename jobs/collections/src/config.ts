// Configuration for the daily collections job, read from the environment and
// validated at startup. A missing required value throws immediately: a
// scheduled job that boots with a blank secret would otherwise fail one layer
// away from the cause (a 401 from PayPal, or a 401 from the app), so we refuse
// to run instead. No value here is ever logged.

/** PayPal environment selector. Sandbox is the default. */
export type PayPalEnvironment = "SANDBOX" | "PRODUCTION";

export interface JobConfig {
  /** Base URL of the PaySay app, e.g. https://paysay.onrender.com. */
  appUrl: string;
  /** Shared secret presented to the app's internal collections endpoint. */
  collectionsToken: string;
  /** PayPal OAuth2 client id. */
  paypalClientId: string;
  /** PayPal OAuth2 client secret. */
  paypalClientSecret: string;
  /** Which PayPal environment to talk to. */
  paypalEnvironment: PayPalEnvironment;
}

/**
 * Reads and validates the job configuration from the environment. Throws with
 * the name of the first missing variable. Never echoes a value.
 */
export function loadConfig(env: NodeJS.ProcessEnv = process.env): JobConfig {
  const read = (name: string): string => {
    const value = env[name];
    if (value === undefined || value.trim() === "") {
      throw new Error(
        `${name} is not configured. Set it via an environment variable before running the collections job.`,
      );
    }
    return value;
  };

  const environmentRaw = (env.PAYPAL_ENVIRONMENT ?? "SANDBOX").toUpperCase();
  if (environmentRaw !== "SANDBOX" && environmentRaw !== "PRODUCTION") {
    throw new Error(`PAYPAL_ENVIRONMENT must be SANDBOX or PRODUCTION, got "${environmentRaw}".`);
  }

  return {
    appUrl: read("APP_URL").replace(/\/+$/, ""),
    collectionsToken: read("COLLECTIONS_TOKEN"),
    paypalClientId: read("PAYPAL_CLIENT_ID"),
    paypalClientSecret: read("PAYPAL_SECRET"),
    paypalEnvironment: environmentRaw,
  };
}
