# PaySay

**Run your business by voice.** PaySay is a voice-first AI assistant that lets
small merchants manage their PayPal invoicing by speaking in their own language.
A market vendor or café owner can say, in German or Thai:

> *"Create an invoice for 30 euros for kunde@example.com"*
> *"Which invoices haven't been paid?"*
> *"Send a reminder for invoice INV2-ABC"*

PaySay understands the request, confirms the money-moving details, calls the
right PayPal tool, and replies in the merchant's language, out loud.

Built for the **PayPal AI Hackathon**. It meaningfully uses both PayPal (the
official PayPal Agent Toolkit / MCP server for real invoicing) and AI (an Amazon
Bedrock Nova agent that reasons, disambiguates, and calls tools).

---

## How it works

```
Browser (mic, Web Speech API)         Go backend
  speech --> text  ──POST /api/chat──▶  Agent loop (Bedrock Converse, tool use)
  audio  ◀─POST /api/speak─────────────  │  intent + disambiguation + confirm
                                         ▼
                             PayPal MCP server (npx @paypal/mcp)
                                         ▼
                             PayPal Sandbox: Invoicing API
```

- **Speech in** is handled in the browser (Web Speech API) in the selected
  language, so there is no fragile server audio stream.
- **The agent** is an Amazon Bedrock **Nova** model driven through the Converse
  API with tool use. Each PayPal tool is surfaced to the model as a tool spec.
- **PayPal** actions run through the official **PayPal MCP server**, which the
  Go backend launches as a subprocess and drives over JSON-RPC (MCP). Tool
  arguments from the model are treated as untrusted and checked against an
  allow-list before any call.
- **Speech out** uses Amazon Polly where a voice exists (German, English); Thai
  gracefully falls back to text.
- **The dashboard** panel is an AG Grid (Community) table of the merchant's
  invoices, fed by `GET /api/invoices` (which calls the PayPal `list_invoices`
  tool) and refreshed automatically after each voice command.

If no PayPal token is configured, PaySay runs in **demo mode** with an
in-memory simulated PayPal provider, so the whole flow is runnable without a
sandbox account.

## Technology & tools

| Area | Technology |
|------|-----------|
| Backend | Go 1.27, standard-library `net/http` |
| AI agent | Amazon Bedrock (Nova Pro) via the Converse API, tool use |
| PayPal | Official PayPal Agent Toolkit / MCP server (`@paypal/mcp`), Invoicing API (sandbox) |
| Protocol | Model Context Protocol (JSON-RPC 2.0 over stdio), hand-written Go client |
| Speech | Browser Web Speech API (STT) + Amazon Polly (TTS) |
| Dashboard | AG Grid (Community) invoice table, sortable/filterable |
| Languages | English, German, Thai (one European + one Asian demo language) |
| Frontend | Accessible vanilla JS/HTML/CSS, embedded in the Go binary |
| Packaging | Docker multi-stage (Go + Node for the MCP server), docker compose |

## Requirements

- Docker (Colima or Docker Desktop) — the only requirement to run.
- For local development without Docker: Go 1.27+ and Node 18+.
- AWS credentials with `bedrock:InvokeModel` (and `polly:SynthesizeSpeech` for
  TTS) for the agent to respond.
- *(Optional)* A PayPal **sandbox** access token for real invoicing.

## Quick start (Docker)

```bash
cp .env.example .env     # fill in AWS creds; PayPal token optional
docker compose up --build
# open http://localhost:8080
```

Use a Chromium-based browser for voice input (Web Speech API). You can also just
type. Pick a language from the selector, then speak or type a request.

## Local development

```bash
cp .env.example .env
set -a && source .env && set +a   # export env into your shell

make run        # start the server on :8080 (go run)
make test       # unit + integration tests
make test-race  # tests with the race detector
make vet        # static analysis
make build      # binary at bin/paysay (web UI embedded)
```

## Configuration

All configuration is via environment variables (see `.env.example`). Key ones:

| Variable | Default | Purpose |
|----------|---------|---------|
| `AWS_REGION` | `us-east-1` | Region for Bedrock + Polly |
| `BEDROCK_MODEL_ID` | `us.amazon.nova-pro-v1:0` | Agent model (use `nova-lite` for lower cost) |
| `BEDROCK_MAX_TOKENS` | `2048` | Max output tokens per turn |
| `PAYPAL_ACCESS_TOKEN` | *(empty)* | Sandbox token; empty = demo/mock mode |
| `PAYPAL_ENVIRONMENT` | `SANDBOX` | `SANDBOX` or `PRODUCTION` |
| `PAYSAY_SUPPORTED_LOCALES` | `en-US,de-DE,th-TH` | Enabled languages |
| `VOICE_TTS_ENABLED` | `true` | Server-side Polly playback |
| `SERVER_ADDR` | `:8080` | Listen address |

> Some Bedrock models require an inference profile id rather than a base model
> id. If you get `on-demand throughput isn't supported`, run
> `aws bedrock list-inference-profiles --region $AWS_REGION` and use that id.

## Getting a PayPal sandbox token

1. Sign in at <https://developer.paypal.com> and create/select an app.
2. Copy the sandbox **Client ID** and **Secret**.
3. Exchange them for an access token:
   ```bash
   curl -s https://api-m.sandbox.paypal.com/v1/oauth2/token \
     -u "CLIENT_ID:SECRET" \
     -d grant_type=client_credentials | jq -r .access_token
   ```
4. Put the token in `.env` as `PAYPAL_ACCESS_TOKEN` and restart.

## Project layout

```
cmd/paysay          entrypoint: wiring + graceful shutdown
internal/config       env configuration + validation
internal/paypal       domain-level PayPal provider (+ mock)
internal/paypal/mcp   MCP JSON-RPC stdio client (PayPal MCP server)
internal/bedrock      Bedrock Converse client wrapper
internal/agent        the tool-use agent loop (the brain)
internal/i18n         locale definitions + localized system prompts
internal/voice        Polly text-to-speech (interface + impl)
internal/server       HTTP handlers, security middleware, invoice dashboard API
internal/web          embedded accessible web UI (incl. AG Grid, self-hosted)
test/e2e              end-to-end flow test (agent + mock PayPal)
```

## Testing

```bash
make test        # all tests (mocked Bedrock + PayPal, no network/credentials)
make cover       # coverage summary
```

Tests cover the MCP JSON-RPC client, the agent tool-use loop, the PayPal
allow-list and mock provider, locale/prompt localization, the HTTP layer
(including security headers and locale detection), and an end-to-end
voice-to-invoice flow.

## Security notes

- Tool arguments produced by the model are untrusted and re-validated against an
  allow-list before any PayPal call; only invoice tools are exposed by default.
- Access tokens are read from the environment and never logged; request bodies
  (which may carry customer details) are never logged.
- Security headers (CSP, `X-Content-Type-Options`, `X-Frame-Options`), a request
  body size limit, a configurable CORS allow-list, and panic recovery are
  applied to every request. The container runs as a non-root user.

## License

MIT — see [LICENSE](LICENSE).
