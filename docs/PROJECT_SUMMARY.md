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
- **Sponsor prizes targeted**: AG Grid (built), Render (planned: hosting,
  Workflows, Cron), APIMatic (planned: PayPal TypeScript SDK in the workflow),
  Bryntum (stretch: collections timeline).

## 2. Architecture

See the README for the diagram. In short: browser speech to text, Go backend
with a Bedrock Nova tool-use agent, an approval gate for money-moving tools,
the PayPal MCP server as a subprocess over JSON-RPC, Polly for spoken replies,
and a vanilla JS merchant panel with AG Grid embedded in the binary.

### HTTP endpoints
`GET /healthz`, `GET /api/config`, `GET /api/locales`, `POST /api/chat`,
`POST /api/confirm`, `POST /api/speak`, `GET /api/invoices` (`?scope=mine`),
`GET /api/invoices/{id}/qr`.

## 3. What is done (as of Nov 7, 2026)

- Merchant panel redesign: sidebar + docked assistant, light editorial theme,
  English/German/Thai throughout, phone layout with bottom tabs.
- AG Grid invoice table and overview grid: status pills, row actions, quick
  filter, status filter, row flash on agent changes, localized formatting.
- Assistant: invoice cards in replies, confirmation cards, quick replies,
  conversation and activity kept in the browser, "New conversation".
- Approval gate in code; single-use, session-bound, 10-minute approvals.
- PayPal token refresh from client credentials; `PAYPAL_ACCESS_TOKEN` removed.
- Seller details stripped from model input and taken from configuration.
- Rate limiting (per visitor and global) and per-session "Created by me".
- Clear "assistant unavailable" error; `make run-docker` fails fast on expired
  SSO.
- Favicon and app icons.

### Verified against the live sandbox
- Token minting, MCP connection, `list_invoices`, `get_invoice`.
- Chat → pending `create_invoice` → Confirm → draft created with the real
  seller account and no invented address.

## 4. What is remaining

- Render: `render.yaml`, deploy, public URL.
- Render Workflows reminder job + Cron Job; APIMatic SDK check inside it.
- AG Grid: totals row, saved column layout, grid texts in German/Thai,
  voice-driven filters.
- Due dates and overdue tracking (needed for the Bryntum timeline).
- Live-test `send_invoice`, `send_invoice_reminder`, payment QR.
- Bryntum collections timeline (stretch).
- Native-speaker review of Thai and German texts.
- First commit and push (nothing committed yet), demo video, Devpost form.
