# PaySay: Project Summary

A single reference for the whole project: the idea, what is built, and what is left.

## 1. The idea

**PaySay** is a voice-first AI assistant that lets small merchants manage PayPal
invoicing by speaking in their own language. The tagline is "pay by saying it".

The problem: small merchants in Europe and Asia often run their business on the
move and do not want to learn a dashboard or type on a small screen in a second
language. PayPal already exposes invoicing through its Agent Toolkit / MCP
server, so the natural interface is conversation, not forms.

The product: a web app where a merchant speaks a command such as
"send Anna an invoice for 30 euros for the garden work", and the assistant:

1. transcribes the speech in the browser,
2. interprets intent with an Amazon Bedrock Nova model,
3. calls PayPal invoicing tools over MCP to draft, confirm, and send the invoice,
4. reads the result back out loud in the merchant's language.

### Scope decisions
- **Audience**: small merchants (not consumers, not remittances).
- **Demo languages**: German, Thai, and English.
- **Approval before money**: creating, sending and reminding require the
  merchant's Confirm on a card. Enforced in code (agent gate + `/api/confirm`),
  not only in the prompt.
- **Demo mode**: without PayPal client credentials the app runs on a mock
  provider. Live mode is badged in the sidebar.
- **Hosted demo**: live sandbox mode on Render, shared by all visitors; "Created
  by me" separates each visitor's invoices.

### Hackathon fit (PayPal AI Hackathon, submission closes Nov 12, 2026)
- **PayPal**: Agent Toolkit / MCP server plus the Invoicing API (real money path).
- **AI**: Amazon Bedrock Nova Pro for intent and tool-use.
- **Sponsor prizes targeted**: AG Grid (built), Render (hosting, Workflows, Cron),
  APIMatic (PayPal TypeScript SDK running Transaction Search in daily collections check),
  Bryntum (collections timeline driven by show_timeline).

## 2. Architecture

See the README for the diagram. In short: browser speech to text, Go backend
with a Bedrock Nova tool-use agent, an approval gate for money-moving tools,
the PayPal MCP server as a subprocess over JSON-RPC, Polly for spoken replies,
and a vanilla JS merchant panel with AG Grid and Bryntum Scheduler embedded in the binary.

### HTTP endpoints
`GET /healthz`, `GET /api/config`, `GET /api/locales`, `POST /api/chat`,
`POST /api/confirm`, `POST /api/speak`, `GET /api/invoices` (`?scope=mine`),
`GET /api/invoices/{id}/qr`, `GET /api/collections`,
`POST /api/collections/approve`, `POST /api/internal/collections` (bearer `COLLECTIONS_TOKEN`).
Non-blocking startup: `GET /healthz` is live immediately; PayPal routes return 503
`paypal_connecting` during background connection; `GET /api/config` exposes `paypal_ready`.

## 3. What is done

- Merchant panel: sidebar + docked assistant, light editorial theme,
  English/German/Thai throughout, phone layout with bottom tabs.
- AG Grid invoice table and overview grid: status pills, row actions, quick
  filter, status filter, row flash on agent changes, pinned totals row, saved
  column state, localized formatting.
- Bryntum Scheduler collections timeline: customer lifecycle rows, payment status
  bars, overdue outlines, Today marker, and screen-driving `show_timeline` tool.
- Assistant: "PaySay Assistant" with voice input, invoice cards in replies,
  confirmation cards, quick replies, chat that follows language switch,
  conversation and activity kept in the browser, "New conversation".
- Approval gate in code: single-use, session-bound, 10-minute approvals.
- PayPal token refresh from client credentials; `PAYPAL_ACCESS_TOKEN` removed.
- Seller details stripped from model input and taken from configuration.
- 14-day due dates set through PayPal Invoicing API directly after draft creation
  (solving the MCP `create_invoice` missing due-date field gap).
- Daily collections check: Render Workflow plus Cron Job in `jobs/collections/`,
  with APIMatic PayPal TypeScript SDK running Transaction Search (confirmed live:
  `invoice_id` holds the invoice number, status `S`).
- Deployment: Render Blueprint (`render.yaml`) hosting the web app, workflow, and cron job;
  live sandbox demo at https://paysay.onrender.com.
- Shared-demo notice for visitors, per-session "Created by me", and tiered rate
  limiting (10/min chat and speech, 30/min invoice list and QR, 300/h shared global
  window, and a ~5s invoice-list cache).
- Clear API error codes: 429 `assistant_busy` (throttled), 503 `assistant_unavailable`
  (unreachable/credentials), 502 `assistant_error` (model/request failure), 504
  `assistant_timeout` (90s chat deadline), and 503 `paypal_connecting`.
- Security hardening: Origin checks on state-changing POSTs (`SERVER_ALLOWED_ORIGINS`),
  server-side confirm allow-list, Permissions-Policy, and TLS-conditional HSTS.
- Static asset caching: embedded UI served with content-hash ETags (304 revalidation)
  and Cache-Control headers (7-day max-age for vendor bundles, `no-cache` elsewhere).
- Bounded timeouts and config cleanup: `SERVER_WRITE_TIMEOUT` default increased to
  120s (exceeding the 90s chat turn deadline), collections job HTTP calls bounded by
  30s timeouts, and dead `VOICE_ENABLED` removed.
- Favicon and app icons.

### Verified against the live sandbox
- Token minting, MCP connection, `list_invoices`, `get_invoice`.
- Chat → pending `create_invoice` → Confirm → draft created with real seller
  account, 14-day due date set via Invoicing API.
- Live `send_invoice`, `send_invoice_reminder`, and payment QR code generation.
- Daily collections check running Transaction Search against live sandbox transactions.

## 4. What is remaining (future ideas)

- More languages: Spanish, French, Japanese, Vietnamese.
- On-device speech: local speech recognition and synthesis for lower latency and offline resilience.
- Recurring invoices: scheduled recurring billing and subscriptions through PayPal subscription tools.
