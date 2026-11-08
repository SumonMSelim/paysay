import { describe, it, expect } from "vitest";
import { loadConfig } from "../src/config.js";

const base = {
  APP_URL: "https://app.example/",
  COLLECTIONS_TOKEN: "a-shared-secret-value",
  PAYPAL_CLIENT_ID: "client-id",
  PAYPAL_SECRET: "client-secret",
};

describe("loadConfig", () => {
  it("reads values and defaults the environment to SANDBOX", () => {
    const cfg = loadConfig(base);
    expect(cfg.appUrl).toBe("https://app.example"); // trailing slash trimmed
    expect(cfg.collectionsToken).toBe("a-shared-secret-value");
    expect(cfg.paypalEnvironment).toBe("SANDBOX");
  });

  it("honours PAYPAL_ENVIRONMENT=PRODUCTION", () => {
    const cfg = loadConfig({ ...base, PAYPAL_ENVIRONMENT: "production" });
    expect(cfg.paypalEnvironment).toBe("PRODUCTION");
  });

  it("rejects an unknown PAYPAL_ENVIRONMENT", () => {
    expect(() => loadConfig({ ...base, PAYPAL_ENVIRONMENT: "STAGING" })).toThrow(/PAYPAL_ENVIRONMENT/);
  });

  it("throws naming the first missing required variable", () => {
    const { COLLECTIONS_TOKEN: _omit, ...withoutToken } = base;
    void _omit;
    expect(() => loadConfig(withoutToken)).toThrow(/COLLECTIONS_TOKEN/);
  });
});
