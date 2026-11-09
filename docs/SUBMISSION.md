# PaySay: Devpost Submission Writeup

> Paste these sections into the matching Devpost fields. Every claim below
> describes what is built and tested today. Sections marked **NOT YET BUILT**
> must be removed or rewritten before submitting.

## Inspiration
Hundreds of millions of small merchants (market vendors, café owners, tutors,
drivers) run their business in a second language and with their hands full.
They lose real income not to fraud but to friction: invoices never written,
unpaid bills never chased, finance tools that assume English and a free hand to
type. We wanted the most natural interface there is: just say what you want, in
your own language, and stay in control of every payment.

## What it does
PaySay is a merchant workspace with a voice assistant for PayPal invoicing.
The merchant speaks in German, Thai or English, and PaySay:

- understands the request with an Amazon Bedrock agent,
- looks up invoices immediately and shows them as cards with customer, amount,
  status and one-tap actions,
- prepares money-moving actions (create an invoice, send it, send a reminder)
  and shows a **confirmation card** with customer, items and total; nothing
  happens until the merchant taps Confirm, and "chase everyone who hasn't
  paid" becomes one card with one tap,
- opens the invoice table already filtered when asked to show invoices,
- runs the approved action through the official **PayPal** MCP server,
- answers out loud in the merchant's language.

Around the assistant is a full workspace: an overview with outstanding and
collected totals and a "needs attention" list, an AG Grid invoice table with
search, filters and per-row actions, a payment QR code for paying at the
counter, and a log of everything the assistant did. The entire interface,
including money and date formats, follows the merchant's language.

## How we built it
- **Backend:** Go with the standard library's `net/http`; layered packages for
  config, agent, PayPal, i18n, voice and HTTP; unit, integration and
  end-to-end tests, all passing with the race detector.
- **AI agent:** Amazon Bedrock **Nova Pro** through the Converse API with tool
  use. The model decides which PayPal tools to call and in what order; it is
  the brain, not a transcription layer.
- **PayPal:** the official **PayPal Agent Toolkit / MCP server**
  (`@paypal/mcp`) driving the **Invoicing API** in the sandbox. The Go backend
  launches it and speaks the **Model Context Protocol** (JSON-RPC 2.0 over
  stdio) through a small hand-written client. Access tokens are minted from the
  app's client credentials and refreshed before expiry.
- **Approval gate:** money-moving tools are held by the agent loop and returned
  as a pending action. The server stores it per browser session with a random,
  single-use ID and a 10-minute expiry; only the merchant's Confirm runs it.
- **Speech:** the browser's Web Speech API for speech to text in the selected
  language; **Amazon Polly** for spoken replies (English, German), with a text
  fallback for Thai.
- **Interface:** a vanilla JS/CSS merchant panel embedded in the Go binary;
  sidebar navigation on desktop, bottom tabs and a full-screen assistant on
  phones; English, German and Thai throughout.
- **Packaging:** multi-stage Docker image (Go binary plus Node for the MCP
  server), one-command Docker Compose.

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
  Search** through the APIMatic-generated PayPal **TypeScript SDK**, so a
  customer who already paid is not reminded. If Transaction Search is
  unavailable, the job degrades gracefully and skips no one.

### NOT YET BUILT: remove or rewrite before submitting
- **Hosted demo URL:** a public deployment via the `render.yaml` Blueprint.

## Challenges we ran into
- **Go meets a TypeScript-first toolkit.** Rather than reimplement PayPal's
  integration, we drove PayPal's MCP server as a subprocess over JSON-RPC,
  keeping a small, auditable surface on a money-moving path.
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
  approving the exact customer, items and total first.
- A complete product experience rather than a chat demo: overview, invoice
  table, activity log and assistant, in three languages, on desktop and phone.
- Security throughout: approval gate, tool allow-list, untrusted-input
  handling, rate limits, no secrets or customer data in logs, CSP, non-root
  container.

## What we learned
How to compose an LLM agent with a third-party MCP tool server safely, why
safety rules belong in code rather than prompts, and how much a
native-language interface changes who a payments product is actually for.

## What's next for PaySay
Due dates and overdue tracking, more languages (Vietnamese, Polish, Tagalog,
Spanish), recurring invoices through PayPal's subscription tools, and on-device
speech for privacy.

## Built with
`go` · `amazon-bedrock` · `amazon-nova` · `amazon-polly` ·
`paypal-invoicing-api` · `paypal-agent-toolkit` · `paypal-transaction-search` ·
`model-context-protocol` · `ag-grid` · `render` · `render-workflows` ·
`apimatic` · `typescript` · `web-speech-api` · `docker`

## Try it out
- GitHub: https://github.com/SumonMSelim/paysay
- Run locally: `cp .env.example .env` (add AWS credentials; PayPal sandbox
  client ID and secret optional), then `docker compose up --build` and open
  `http://localhost:8085`.
- Hosted demo: **NOT YET BUILT**, add the Render URL here.
