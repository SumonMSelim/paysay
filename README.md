# PaySay

**Run your business by voice.** PaySay is a merchant workspace with a built-in
voice assistant for PayPal invoicing. A market vendor or café owner speaks in
their own language, in German, Thai or English, and PaySay creates, sends and
chases PayPal invoices for them:

> *"Erstelle eine Rechnung über 30 Euro an kunde@example.com für Gartenarbeit"*
> *"มีใบแจ้งหนี้ไหนที่ยังไม่ได้ชำระบ้าง"* (Which invoices are still unpaid?)
> *"Send a reminder for my latest invoice"*

The assistant understands the request, calls the right PayPal tool, and answers
out loud in the merchant's language. Nothing that moves money happens until the
merchant taps **Confirm** on a card showing the customer, items and total.

Built for the **PayPal AI Hackathon**: it uses PayPal (the official PayPal Agent
Toolkit / MCP server and the Invoicing API) and AI (an Amazon Bedrock Nova agent
that reasons and calls tools) together, not side by side.

---

## What the merchant sees

- **Overview:** outstanding and collected totals, drafts, collection rate, and
  a "Needs attention" list with one-tap Remind / Send.
- **Invoices:** an AG Grid table with status pills, search, status filter
  (draft, awaiting payment, paid), a "Created by me" toggle, per-row Send,
  Remind and payment-QR actions, and a pinned totals row that follows the
  filters. Rows the assistant just changed flash briefly, and the column
  layout is remembered. Saying *"show me Anna's unpaid invoices"* opens the
  table already filtered.
- **Timeline:** a Bryntum Scheduler view of customer payment lifecycles. One
  resource row per customer with bars spanning invoice creation to due (or
  paid) date, color-coded by status with distinct red outlines for overdue
  invoices, an overdue summary count, and bar clicks triggering invoice
  QR/actions. Saying *"show me Anna's payment timeline"* opens the timeline
  filtered to that customer and scrolls directly to their invoices.
- **Agent activity:** every PayPal action the assistant took, with details.
- **Assistant (docked on the right, full screen on phones):** a large
  *Tap and speak* button, example requests in the merchant's language, invoice
  cards in replies, and confirmation cards before any money-moving action.
  Several actions (for example a reminder per unpaid invoice) share one card
  and one tap.

The whole panel, not just the replies, follows the selected language,
including money and date formats (for example `328,00 €` in German and
Buddhist-calendar dates in Thai).

## How it works

```
Browser (Web Speech API)                Go backend
  speech -> text  ──POST /api/chat──▶   Agent loop (Bedrock Converse, tool use)
                                         │  read-only tools run immediately
                                         │  money-moving tools stop here ──┐
  confirmation card ◀─── pending action ─┘                                  │
  merchant taps Confirm ──POST /api/confirm──▶ run exactly that action ◀────┘
                                         ▼
                             PayPal MCP server (@paypal/mcp)
                                         ▼
                             PayPal Sandbox: Invoicing API
  spoken reply ◀──POST /api/speak── Amazon Polly
```

- **The agent** is Amazon Bedrock **Nova Pro** driven through the Converse API
  with tool use. Each PayPal tool is offered to the model as a tool spec.
- **PayPal** actions run through the official **PayPal MCP server**, launched
  by the Go backend as a subprocess and driven over JSON-RPC (MCP).
- **Approval is enforced in code, not by prompt.** `create_invoice`,
  `send_invoice` and `send_invoice_reminder` never run from the model's turn.
  The agent stops and returns a pending action; the server keeps it under the
  visitor's session with a random, single-use ID that expires after 10 minutes.
  Only `POST /api/confirm` with that ID runs it. The browser never supplies the
  action itself.
- **The assistant can drive the screen.** A screen-only `show_invoices` tool
  lets the agent open the invoice table with a status and customer filter. It
  never contacts PayPal.
- **Seller details come from configuration.** The model tends to invent a
  business name and address; the server strips any seller fields it supplies
  and fills them from `PAYPAL_MERCHANT_EMAIL` / `PAYPAL_MERCHANT_NAME`.
- **Speech in** uses the browser's Web Speech API in the selected language.
  **Speech out** uses Amazon Polly where a voice exists (English, German); Thai
  falls back to text.
- **PayPal tokens** are minted from the sandbox client ID and secret and
  refreshed before they expire, restarting the MCP server with the new token.

Without PayPal credentials, PaySay runs in **demo mode** with an in-memory
simulated PayPal provider, clearly badged in the sidebar.

## Technology

| Area | Technology |
|------|-----------|
| Backend | Go 1.27, standard-library `net/http` |
| AI agent | Amazon Bedrock (Nova Pro) via the Converse API, tool use |
| PayPal | PayPal Agent Toolkit / MCP server (`@paypal/mcp`), Invoicing API (sandbox) |
| Protocol | Model Context Protocol (JSON-RPC 2.0 over stdio), hand-written Go client |
| Speech | Browser Web Speech API (speech to text), Amazon Polly (text to speech) |
| Invoice table | AG Grid Community v33, self-hosted |
| Timeline | Bryntum Scheduler v7.3.8 (commercial trial, fetched at build time) |
| Languages | English, German, Thai |
| Frontend | Vanilla JS/HTML/CSS embedded in the Go binary, no build step |
| Packaging | Docker multi-stage (Go + Node for the MCP server), Docker Compose |

> Bryntum Scheduler is fetched at Docker build time under its commercial trial
> license (`@bryntum/scheduler-trial`), with prebuilt browser bundles embedded
> into the Go binary. When the files are absent (such as after a plain host
> build), the Timeline degrades gracefully with a localized fallback message.

## Requirements

- Docker (Colima or Docker Desktop).
- AWS credentials with `bedrock:InvokeModel` and `polly:SynthesizeSpeech`.
- *(Optional)* A PayPal **sandbox** app's client ID and secret for live
  invoicing. Without them, PaySay runs in demo mode.
- Chrome or Edge for voice input. Typing works in any browser.

## Quick start (Docker)

```bash
cp .env.example .env     # add AWS credentials; PayPal sandbox app optional
docker compose up --build
# open http://localhost:8085  (set PORT in .env to change it)
```

With an AWS SSO profile, `make run-docker` exports temporary credentials into
Compose for you (run `make login` first if the session expired).

## Local development

```bash
make run-docker   # Docker Compose with SSO credentials from AWS_PROFILE
make test         # unit, integration and end-to-end tests (no network needed)
make test-race    # the same with the race detector
make vet          # static analysis
make build        # binary at bin/paysay with the web UI embedded
make help         # all targets
```

## Configuration

All configuration is via environment variables (see `.env.example`).

| Variable | Default | Purpose |
|----------|---------|---------|
| `AWS_REGION` | `us-east-1` | Region for Bedrock and Polly |
| `BEDROCK_MODEL_ID` | `us.amazon.nova-pro-v1:0` | Agent model |
| `BEDROCK_MAX_TOKENS` | `2048` | Max output tokens per turn |
| `PAYPAL_CLIENT_ID` / `PAYPAL_SECRET` | *(empty)* | Sandbox app credentials; empty = demo mode |
| `PAYPAL_ENVIRONMENT` | `SANDBOX` | `SANDBOX` or `PRODUCTION` |
| `PAYPAL_MERCHANT_EMAIL` | *(empty)* | Sandbox business email used as the seller |
| `PAYPAL_MERCHANT_NAME` | *(empty)* | Business name on invoices; empty = PayPal account profile |
| `PAYSAY_SUPPORTED_LOCALES` | `en-US,de-DE,th-TH` | Enabled languages |
| `VOICE_TTS_ENABLED` | `true` | Spoken replies via Polly |
| `RATE_LIMIT_PER_MINUTE` | `10` | Chat and speech requests per visitor per minute (0 = off) |
| `RATE_LIMIT_PER_HOUR` | `300` | Chat and speech requests across all visitors per hour (0 = off) |
| `SERVER_ADDR` | `:8080` | Listen address inside the container |
| `COLLECTIONS_TOKEN` | *(empty)* | Shared secret for the daily collections endpoint; empty = endpoint disabled (404). At least 16 characters when set |

> Some Bedrock models need an inference profile ID rather than a base model
> ID. If you get `on-demand throughput isn't supported`, run
> `aws bedrock list-inference-profiles --region $AWS_REGION` and use that ID.

## Connecting a PayPal sandbox account

1. Sign in at <https://developer.paypal.com> and create or select an app.
2. Copy the sandbox **Client ID** and **Secret**.
3. Put them in `.env` as `PAYPAL_CLIENT_ID` and `PAYPAL_SECRET`, set
   `PAYPAL_MERCHANT_EMAIL` to the sandbox business account's email, and restart.

The sidebar then shows **PayPal sandbox · live**. PayPal's sandbox does not
deliver real emails; sent invoices appear under the sandbox account's
notifications on developer.paypal.com.

## Daily collections check

A scheduled job checks once a day which invoices still need chasing, so the
merchant does not have to. It is a **Render Workflow**, triggered daily by a
**Render Cron Job**, and it lives in `jobs/collections/` (TypeScript, Node 22).

Each run:

1. Reads the merchant's invoices from the app's `GET /api/invoices` and picks
   the sent-but-unpaid ones (`SENT`, `UNPAID`, `PARTIALLY_PAID`) created more
   than three days ago. Drafts are never chased.
2. Cross-checks PayPal **Transaction Search** over the last 31 days (through the
   APIMatic-generated PayPal TypeScript SDK) so a customer who already paid is
   not reminded. Matching is on the transaction's `invoice_id` with a completed
   (`S`) status.
3. Posts the result to the app's internal endpoint. The merchant then sees an
   Overview card, *"Daily check: N invoices to chase"*, with one **Approve**
   button.

The job never sends reminders itself. Approval goes through the same
confirmation gate as every other money-moving action: the Approve button reuses
the existing confirmation card and `POST /api/confirm`.

If Transaction Search is unavailable (for example a 403 because the feature is
not enabled on the PayPal app), the job still posts a plan with no skips and
logs a warning, rather than failing.

> **Transaction Search caveats.** The PayPal sandbox app must have the
> **Transaction Search** feature enabled (developer.paypal.com → your app →
> Features). Sandbox transactions can also take up to about **3 hours** to
> become searchable, so a very recent payment may not be reconciled on the
> next run.

The job and the app share the `COLLECTIONS_TOKEN` secret. The job also needs:

| Variable | Purpose |
|----------|---------|
| `APP_URL` | Base URL of the PaySay app, e.g. `https://paysay.onrender.com` |
| `COLLECTIONS_TOKEN` | Shared secret presented to the internal collections endpoint |
| `PAYPAL_CLIENT_ID` / `PAYPAL_SECRET` | Sandbox app credentials for Transaction Search |
| `PAYPAL_ENVIRONMENT` | `SANDBOX` (default) or `PRODUCTION` |
| `RENDER_API_KEY` | *(cron only)* Triggers the workflow run |
| `WORKFLOW_SLUG` | *(cron only)* The workflow service's slug |

Deployment is described in `render.yaml` (a Render Blueprint). Render Workflows
can be declared in a Blueprint, so all three services (web, workflow, cron) are
defined there.

Run the job's checks locally with Docker:

```bash
cd jobs/collections
docker run --rm -v "$PWD":/job -w /job node:22 sh -c "npm install && npm run build && npm test"
```

## Project layout

```
cmd/paysay            entrypoint: wiring and graceful shutdown
internal/config       environment configuration and validation
internal/agent        the tool-use agent loop and the approval gate
internal/bedrock      Bedrock Converse client wrapper
internal/paypal       PayPal provider (live and mock), token refresh, argument shaping
internal/paypal/mcp   MCP JSON-RPC stdio client
internal/i18n         locales and localized system prompts
internal/voice        Polly text to speech
internal/server       HTTP API, approvals, sessions, rate limiting, security middleware
internal/web          embedded web UI (HTML/CSS/JS, translations, icons, AG Grid)
jobs/collections      daily collections check: Render Workflow + Cron Job (TypeScript)
test/e2e              end-to-end flow: chat, approval, invoice created
```

## Security

- Money-moving actions need an explicit approval tied to the visitor's session
  (single use, 10-minute expiry); the model cannot run them itself.
- Tool arguments from the model are untrusted: tools are checked against an
  allow-list, and seller details are replaced with configured values.
- Per-visitor and global rate limits cap AWS spend on a public deployment.
- Credentials come from the environment and are never logged; request bodies,
  which may contain customer details, are never logged.
- CSP, `X-Content-Type-Options`, `X-Frame-Options`, a body size limit, a CORS
  allow-list and panic recovery apply to every request. The container runs as
  a non-root user.
- Conversation history stays in the visitor's browser; the server keeps none.

## License

MIT, see [LICENSE](LICENSE).
