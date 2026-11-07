# PaySay — Devpost Submission Writeup

> Paste these sections into the matching fields on the Devpost submission form.
> Before submitting, run the live PayPal sandbox path so the claims below match
> reality. If you demo in mock mode, soften "a real PayPal sandbox invoice" to
> "a PayPal invoice via the sandbox integration."

## Inspiration
Hundreds of millions of small merchants, market vendors, café owners, tutors,
drivers, run their businesses in a second language and with their hands full.
They lose real income not to fraud but to friction: invoices never created,
unpaid bills never chased, finance apps that assume English and a free hand to
type. We wanted to collapse all of that into the most natural interface there
is: just saying what you want, in your own language.

## What it does
PaySay is a voice-first AI assistant for small-merchant payments. A merchant
speaks a request in German, Thai, or English, and PaySay:
- understands the intent,
- confirms the money-moving details (recipient, amount, currency),
- calls the right **PayPal** tool to create, send, look up, or send a reminder
  for an invoice,
- and replies back in the merchant's language, out loud.

Example spoken commands:
- *"Create an invoice for 30 euros for kunde@example.com and send it."* (German)
- *"Are there any unpaid invoices?"* (Thai)
- *"Send a reminder for invoice INV2-ABC."*

If a request is ambiguous, PaySay asks a clarifying question instead of
guessing, because it moves real money.

## How we built it
- **Backend:** Go (standard-library `net/http`), clean layered architecture,
  ~3,400 lines, fully tested.
- **AI agent:** Amazon **Bedrock** (Nova Pro) via the Converse API with tool
  use. The model reasons about the request and decides which PayPal tools to
  call, this is the "brain", not just transcription.
- **PayPal:** the official **PayPal Agent Toolkit / MCP server** (`@paypal/mcp`),
  driving the **Invoicing API** in the sandbox. Our Go backend launches the MCP
  server and speaks the **Model Context Protocol** (JSON-RPC 2.0 over stdio) to
  it via a hand-written, auditable client.
- **Speech:** browser Web Speech API for speech-to-text in the selected
  language; **Amazon Polly** for spoken replies where a voice exists (German,
  English), with graceful text fallback for Thai.
- **Dashboard:** an **AG Grid** invoice table in the activity panel, so the
  merchant can sort and filter their invoices (e.g. show only unpaid).
- **Frontend:** an accessible web UI (skip links, ARIA live regions, keyboard
  support, reduced-motion support), embedded directly in the Go binary.
- **Packaging:** multi-stage Docker (Go + Node for the MCP server), one-command
  `docker compose up`.

## How AI and PayPal work together (Tools field)
AI and PayPal are not side by side, they are wired together. The PayPal MCP
tools are surfaced to the Bedrock model as tool specifications; the model emits
a tool-use request; our Go orchestrator validates it against a safety allow-list
and forwards it to PayPal; the result is fed back to the model, which continues
until it has a final answer. The agent only moves money through PayPal, and only
through an allow-listed set of invoice tools.

- **AI tool used:** Amazon Bedrock (Nova Pro) via the Converse API, with tool
  use / function calling. Amazon Polly for text-to-speech.
- **PayPal technology used:** PayPal Agent Toolkit / MCP server and the PayPal
  Invoicing API (sandbox).
- **Sponsor tool used:** AG Grid (Community) for the invoice dashboard.

## Challenges we ran into
- Bridging a **Go** backend to PayPal's **TypeScript-first** agent toolkit. We
  drove PayPal's MCP server as a subprocess over JSON-RPC rather than
  reimplementing the integration, keeping a small, auditable dependency surface
  on a money-moving path.
- Making the AI do more than speech-to-text: real intent understanding,
  disambiguation, and safe confirmation across languages.
- Multilingual output: Polly has no standard Thai voice, so TTS degrades
  gracefully to text per locale.

## Accomplishments that we're proud of
- A real, end-to-end agentic-commerce flow: one spoken sentence -> a real PayPal
  sandbox invoice.
- Security built in from the start: untrusted-tool-input validation, tool
  allow-listing, no secrets or customer data in logs, CSP and other hardening,
  non-root container.
- Runnable by anyone: it works even without a PayPal token via a built-in
  simulated provider, clearly badged as demo mode.

## What we learned
How to compose an LLM agent with a third-party MCP tool server safely, and how
much a native-language voice interface changes who a payments product is
actually *for*.

## What's next for PaySay
More languages (Vietnamese, Polish, Tagalog, Spanish), a richer invoice
dashboard, recurring invoices and subscriptions via PayPal's subscription tools,
and on-device speech for privacy.

## Built with
`go` · `amazon-bedrock` · `amazon-nova` · `amazon-polly` · `paypal-invoicing-api`
· `paypal-agent-toolkit` · `model-context-protocol` · `ag-grid` ·
`web-speech-api` · `docker`

## Try it out
- GitHub: https://github.com/SumonMSelim/paysay
- Setup: `cp .env.example .env` (add AWS creds; PayPal sandbox token optional)
  -> `docker compose up --build` -> open `http://localhost:8080`
