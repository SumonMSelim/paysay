# PaySay: Devpost Submission Writeup

> Paste these sections into the matching Devpost fields. Every claim below
> describes what is built and tested today.

## Inspiration
Hundreds of millions of small merchants (market vendors, café owners, tutors,
drivers) run their business in a second language and with their hands full.
They lose real income not to fraud but to friction: invoices never written,
unpaid bills never chased, finance tools that assume English and a free hand to
type. We wanted the most natural interface there is: just say what you want, in
your own language, and stay in control of every payment.

## What it does
PaySay is a merchant workspace with a voice assistant ("PaySay Assistant") for PayPal invoicing.
The merchant speaks or types in German, Thai or English, and PaySay:

- understands the request with an Amazon Bedrock agent,
- looks up invoices immediately and shows them as cards with customer, amount,
  status and one-tap actions,
- prepares money-moving actions (create an invoice, send it, send a reminder)
  and shows a **confirmation card** with customer, items, total and 14-day due date;
  nothing happens until the merchant taps Confirm, and "chase everyone who hasn't
  paid" becomes one card with one tap,
- opens the invoice table or the **Bryntum timeline** already filtered when asked
  to show invoices or view a customer's payment lifecycle (driven by the
  `show_invoices` and `show_timeline` tools),
- runs approved actions through the official **PayPal** MCP server, setting 14-day
  due dates through the Invoicing API to bridge the MCP tool's due-date gap,
- answers out loud in the merchant's language (via Amazon Polly in English and German;
  Thai replies follow the language switch in text).

Around the assistant is a full workspace: an overview with outstanding and
collected totals, a daily collections check card (reconciled against PayPal
Transaction Search), an AG Grid invoice table with search, filters and per-row
actions, a Bryntum Scheduler collections timeline with overdue outlines,
a payment QR code for paying at the counter, and an activity log. The entire
interface, including the chat and date formats (such as the Buddhist calendar in Thai),
follows the language switch instantly.

## How we built it
- **Backend:** Go with the standard library's `net/http`; layered packages for
  config, agent, PayPal, i18n, voice and HTTP; non-blocking startup with background
  PayPal connection (`/healthz` is up immediately, PayPal routes return 503
  `paypal_connecting` during connect, `/api/config` exposes `paypal_ready`); unit,
  integration and end-to-end tests, all passing with the race detector.
- **AI agent:** Amazon Bedrock **Nova Pro** through the Converse API with tool
  use. The model decides which PayPal tools to call and in what order; it is
  the brain, not a transcription layer. The chat follows language switches dynamically.
  Failures map to an explicit error taxonomy: 429 `assistant_busy`, 503
  `assistant_unavailable`, 502 `assistant_error`, and 504 `assistant_timeout` (on a 90s
  turn deadline, backed by a 120s server write timeout).
- **PayPal:** the official **PayPal Agent Toolkit / MCP server**
  (`@paypal/mcp`) driving the **Invoicing API** in the sandbox. The Go backend
  launches it and speaks the **Model Context Protocol** (JSON-RPC 2.0 over
  stdio) through a small hand-written client. Access tokens are minted from the
  app's client credentials and refreshed before expiry. Because the MCP
  `create_invoice` tool has no due-date field (confirmed live), the backend sets each new
  invoice's 14-day due date with a direct Invoicing API update right after the draft
  is created.
- **Daily collections check:** a scheduled job built with Node 22 and TypeScript in
  `jobs/collections/`. It runs on Render as a **Render Workflow** triggered by a
  **Render Cron Job**, using APIMatic's PayPal TypeScript SDK to execute Transaction
  Search (confirmed live: `invoice_id` holds the invoice number, status `S`) so customers
  who already paid are excluded before presenting a single-tap Approve card. HTTP
  requests to PaySay are bounded by 30-second timeouts.
- **Timeline:** a **Bryntum Scheduler** timeline driven by the screen-only `show_timeline`
  tool, displaying customer rows, status-colored invoice bars, red outlines for overdue
  invoices, and a non-covering Today marker with localized month-band pills.
- **Approval gate & security:** money-moving tools are held by the agent loop and returned
  as a pending action. The server stores it per browser session with a random,
  single-use ID and a 10-minute expiry; only the merchant's Confirm runs it against a
  server allow-list. State-changing POSTs enforce Origin checks (`SERVER_ALLOWED_ORIGINS`).
  Tiered rate limits (10/min chat/speech, 30/min invoice list/QR, 300/h shared global window,
  5s invoice cache), HSTS, and Permissions-Policy protect the service.
- **Speech:** the browser's Web Speech API for speech to text in the selected
  language; **Amazon Polly** for spoken replies (English, German), with a text
  fallback for Thai.
- **Interface:** a vanilla JS/CSS merchant panel embedded in the Go binary with
  content-hash ETags (304 revalidation) and Cache-Control headers; sidebar navigation
  on desktop, bottom tabs and a full-screen assistant on phones; English, German and Thai
  throughout.
- **Packaging:** multi-stage Docker image (Go binary plus Node for the MCP
  server), one-command Docker Compose, and a unified `render.yaml` Blueprint.

## How AI and PayPal work together (Tools field)
The PayPal MCP tools are offered to the Bedrock model as tool specifications.
The model emits a tool-use request; the Go orchestrator checks it against an
allow-list. Read-only tools (list and look up invoices) run immediately and
their results go back to the model, which continues until it has an answer.
Money-moving tools stop the loop and become a confirmation card; only the
merchant's approval sends them to PayPal, with seller details taken from
configuration rather than from the model.

- **AI used:** Amazon Bedrock (Nova Pro), Converse API with tool use; Amazon
  Polly for text to speech.
- **PayPal technology used:** PayPal Agent Toolkit / MCP server; PayPal
  Invoicing API (sandbox): create, send, list, look up, remind, payment QR.
- **Sponsor tools used:**
  - **AG Grid Community v33:** for the invoice table and the overview's recent
    invoices. The agent drives the grid: a screen-only `show_invoices` tool
    turns "show me Anna's unpaid invoices" into a status and quick filter.
    Also: status pill renderers, per-row action cells, an external status
    filter, a pinned totals row that follows filters (with per-currency sums),
    row flashing when the agent changes an invoice, saved column state, and
    localized value formatters.
  - **Bryntum Scheduler v7.3.8:** a read-only collections timeline showing one
    resource row per customer and event bars spanning invoice creation to due or
    paid date. Bars match the payment status palette with distinct red outlines
    for overdue invoices, and bar clicks trigger invoice actions. The agent
    drives the schedule: a screen-only `show_timeline` tool lets voice commands
    like *"show me Anna's payment timeline"* filter and scroll the timeline to
    that customer. Prebuilt UMD bundle with full English, German and Thai
    localization; degrades gracefully when assets are absent.
- **Render:** a daily collections check runs as a **Render Workflow**, triggered
  once a day by a **Render Cron Job** (Render Workflows has no native scheduler
  yet). All three services, the web app, the workflow and the cron job, are
  declared in a `render.yaml` Blueprint. The job finds sent-but-unpaid invoices,
  and the merchant sees an Overview card, *"Daily check: N invoices to chase"*,
  with one Approve button. The job never sends reminders itself: approval goes
  through the app's existing confirmation gate.
- **APIMatic:** the collections job reconciles against PayPal **Transaction
  Search** through the APIMatic-generated PayPal **TypeScript SDK** (confirmed live:
  `invoice_id` holds the invoice number, status `S`), so a
  customer who already paid is not reminded. If Transaction Search is
  unavailable, the job degrades gracefully and skips no one.

## Challenges we ran into
- **Go meets a TypeScript-first toolkit.** Rather than reimplement PayPal's
  integration, we drove PayPal's MCP server as a subprocess over JSON-RPC,
  keeping a small, auditable surface on a money-moving path.
- **The MCP create_invoice due-date gap:** The official PayPal MCP `create_invoice`
  tool has no due-date field. Because invoices require due dates for overdue tracking
  and the Bryntum timeline, the Go backend immediately follows draft creation with a
  direct PayPal Invoicing API update that sets the due date (defaulting to 14 days after
  the invoice date). This was confirmed and verified live in the sandbox.
- **An agent that moves money must not act alone.** Asking for confirmation in
  the prompt was not enough, so approval became a hard gate in code.
- **Models invent what they don't know.** In the live sandbox, Nova filled the
  seller's business name and address with placeholders. We now strip
  model-supplied seller fields and use configured values.
- **Live and mock shapes differ.** PayPal's real responses nest fields (for
  example the creation time under `detail.metadata`), so we normalize both.
- **Multilingual output:** Polly has no standard Thai voice, so speech degrades
  gracefully to text, and dates follow the Buddhist calendar in Thai.

## Accomplishments that we're proud of
- One spoken sentence becomes a real PayPal sandbox invoice, with the merchant
  approving the exact customer, items, total and 14-day due date first.
- A complete product experience rather than a chat demo: overview, daily collections
  card, Bryntum timeline, invoice table, activity log and assistant, in three languages,
  on desktop and phone.
- Security and resilience throughout: approval gate, server tool allow-list,
  non-blocking startup with background PayPal connection, Origin checks, shared
  rate limits, security headers (HSTS, Permissions-Policy), clear error taxonomy,
  no secrets or customer data in logs, CSP, non-root container.

## What we learned
How to compose an LLM agent with a third-party MCP tool server safely, why
safety rules belong in code rather than prompts, and how much a
native-language interface changes who a payments product is actually for.

## What's next for PaySay
More languages (Vietnamese, Polish, Tagalog, Spanish), recurring invoices
through PayPal's subscription tools, and on-device speech for privacy and lower latency.

## Built with
`go` · `amazon-bedrock` · `amazon-nova` · `amazon-polly` ·
`paypal-invoicing-api` · `paypal-agent-toolkit` · `paypal-transaction-search` ·
`model-context-protocol` · `ag-grid` · `bryntum-scheduler` · `render` · `render-workflows` ·
`apimatic` · `typescript` · `web-speech-api` · `docker`

## Try it out
- Hosted demo: https://paysay.onrender.com (live PayPal sandbox).
- GitHub: https://github.com/SumonMSelim/paysay
- Run locally: `cp .env.example .env` (add AWS credentials; PayPal sandbox
  client ID and secret optional), then `docker compose up --build` and open
  `http://localhost:8085`.

### Note for judges
All judges share a single live PayPal sandbox account. Because PayPal sandbox never
delivers emails to real inboxes, please do **not** enter real email addresses.

**Three quick things to try:**
1. **German create flow:** Switch the language dropdown to **Deutsch** (the entire UI
   and chat switch instantly). Speak or type:
   > *"Erstelle eine Rechnung über 30 Euro an kunde@example.com für Gartenarbeit"*
   Review the confirmation card with its calculated 14-day due date, tap **Rechnung erstellen**,
   and watch the real sandbox invoice appear with one-tap Send confirmation.
2. **Timeline by voice:** Say or type *"Zeig mir die Zahlungszeitachse von Anna"*
   (or in English: *"Show me Anna's payment timeline"*). PaySay opens the Bryntum
   timeline, filters to Anna, and displays invoice bars with distinct red outlines
   for overdue invoices.
3. **Thai queries:** Switch to **ไทย** and ask:
   > *"มีใบแจ้งหนี้ไหนที่ยังไม่ได้ชำระบ้าง"* (Which invoices are still unpaid?)
   The assistant lists unpaid invoices with dates formatted according to the Buddhist
   calendar, accompanied by one-tap reminder actions.
