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
- **Confirmation before money**: the agent always asks before it creates or sends
  an invoice. This is a deliberate safety design, not a bug.
- **Demo mode**: the app runs with a mock PayPal provider when no sandbox token is
  present, so it is always demoable. Live mode is badged in the UI.

### Hackathon fit (PayPal AI Hackathon, submission closes Nov 12, 2026)
- **PayPal**: Agent Toolkit / MCP server plus the Invoicing API (real money path).
- **AI**: Amazon Bedrock Nova Pro for intent and tool-use.
- **Extra prize target**: "Best Use of AG Grid" via a self-hosted invoice dashboard.

## 2. Architecture

- **Backend**: Go (`github.com/SumonMSelim/paysay`).
- **Model**: Amazon Bedrock Nova Pro `us.amazon.nova-pro-v1:0` via the Converse
  API with tool-use (Amazon first-party model, per the AWS credits available).
- **Speech-to-text**: browser Web Speech API (robust, no server round-trip).
- **Text-to-speech**: Amazon Polly. German uses the neural voice "Vicki"; Thai has
  no Polly voice, so it falls back to on-screen text.
- **PayPal integration (Option A)**: the Go backend launches the PayPal MCP server
  (`npx -y @paypal/mcp --tools=all`) as a subprocess and talks to it over
  JSON-RPC on stdio using a small hand-written client. Direct REST is kept as a
  documented fallback (Option B).
- **Packaging**: Docker, built and run locally with Colima.

### Layout
```
cmd/paysay/main.go            entry point
internal/config               env + flags
internal/bedrock              Nova Converse client
internal/agent                agent loop, reply cleaning, tool orchestration
internal/paypal               invoicing service
internal/paypal/mcp           JSON-RPC MCP client
internal/i18n                 de / th / en strings
internal/voice                Polly TTS
internal/server               HTTP server, security middleware, handlers
internal/web                  static asset serving
web/assets                    index.html, styles.css, app.js, AG Grid vendor bundle
test/e2e                      end-to-end flow test
docs                          DEMO_SCRIPT, SUBMISSION, this summary
```

### HTTP endpoints
`GET /healthz`, `GET /api/config`, `GET /api/locales`, `POST /api/chat`,
`POST /api/speak`, `GET /api/invoices`.
Security middleware: CSP, X-Frame-Options, request body limit, CORS, panic recovery.

## 3. What is done

- Full Go implementation across all packages listed above.
- Amazon Nova Pro agent loop with Converse tool-use, wired to PayPal invoicing
  tools (`create_invoice`, `send_invoice`, `list_invoices`, `get_invoice`,
  `send_invoice_reminder`, `generate_invoice_qr_code`, `get_order`) via an
  allow-list.
- Mock PayPal provider so the app runs end-to-end without a token.
- i18n for German, Thai, English; Polly TTS with the German neural voice and a
  Thai text fallback.
- Accessible web UI plus an AG Grid Community v33 invoice dashboard, self-hosted
  (not CDN) to comply with the strict CSP.
- Docker image and docker-compose, targeting Colima.
- End-to-end and unit tests. Full suite passes with `-race`; `go vet ./...` clean.
- README, demo script (`docs/DEMO_SCRIPT.md`), and Devpost writeup
  (`docs/SUBMISSION.md`).
- Makefile helpers: `run-local`, `run-docker`, `token`, `login`, `help`.
- MIT `LICENSE` at the repo root (hackathon requirement).

### Verified against live APIs
- AWS Bedrock Nova Pro Converse works via the `sumonmselim` SSO profile.
- Amazon Polly neural voice "Vicki" works.
- A real German chat turn produced a clean reply with a correct `30.00 USD` total.

### Bugs found and fixed during live testing
- Nova leaked `<thinking>...</thinking>` into replies. Fixed with `cleanReply()`
  and a `thinkingBlock` regexp in `agent.go`.
- Invoice total came back as `0.00`. Fixed with `sumItems()` / `parseAmount()` in
  `mock.go`, deriving the total from `items[].unit_amount x quantity` (handles the
  nested `{"value": ...}` shape).

## 4. What is remaining

### Blocking the "both real APIs" claim
- **Live PayPal path is unverified.** Everything else runs on real APIs; only the
  PayPal sandbox link is untested. PayPal's real `create_invoice` schema is
  stricter than the mock (nested `detail`, `items`, `primary_recipients`), so the
  model's tool arguments may need a shaping layer or a prompt tweak once we see a
  real rejection.

### User steps to unblock
1. Create a PayPal sandbox app with Invoicing enabled.
2. Put `PAYPAL_CLIENT_ID` / `PAYPAL_SECRET` in `.env`.
3. Run `eval "$(make -s token)"` to mint `PAYPAL_ACCESS_TOKEN` (expires ~9h).
4. Set `PAYPAL_MERCHANT_EMAIL`.
5. Run `make run-local` and confirm the startup log shows `"paypal_live":true`.
6. Try a live `create_invoice` turn and capture the "Assistant actions" output
   (especially any error JSON) so the argument shaping can be fixed if PayPal
   rejects the model's arguments.

### After live PayPal works
- `git commit` and push to `github.com/SumonMSelim/paysay` (nothing is committed
  yet, by request).
- Record a demo video under 3 minutes.
- Fill in the Devpost submission form.

### Optional / nice to have
- Auto-execute flag for low-risk actions, to make the demo snappier (removes the
  second confirmation turn).
- `make run-docker` end-to-end run on Colima with exported SSO credentials.

## 5. Quick reference

- Workspace: `/Users/sumonmselim/Work/paysay`
- Go 1.27 at `/opt/homebrew/bin` (`export PATH="/opt/homebrew/bin:$PATH"`).
- Verify: `go vet ./...` then `go test ./...`.
- Run locally: `make run-local` (sets `AWS_PROFILE=sumonmselim`).
- Key env: `BEDROCK_MODEL_ID=us.amazon.nova-pro-v1:0`, `BEDROCK_MAX_TOKENS=2048`,
  `PAYPAL_ACCESS_TOKEN`, `PAYPAL_ENVIRONMENT=SANDBOX`, `PAYPAL_MERCHANT_EMAIL`,
  `VOICE_TTS_ENABLED`, `AWS_REGION`.
