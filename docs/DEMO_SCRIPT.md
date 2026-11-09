# PaySay: Demo Video Script (target 2:50)

Judges may stop at 3:00, so the problem and a working end-to-end flow must land
early. Target: 2 minutes 50 seconds.

---

## [0:00–0:18] Problem
*(Voiceover over a busy market stall or café counter.)*

> "Small merchants lose money every day, not to fraud, but to friction. They're
> too busy serving customers on the move to type invoices, chase unpaid bills, or
> fight complex finance dashboards. What if they could just run their business
> by voice, in their own language, while staying in complete control?"

## [0:18–0:32] Overview and the daily-check card
*(PaySay open on the Overview in English. Cursor highlights the KPI cards and the daily-check card.)*

> "This is PaySay. At a glance: outstanding money, drafts, and a daily collections
> check. Every morning a background job reconciles unpaid invoices against PayPal
> Transaction Search so customers who already paid are never bothered. And nothing
> is ever chased without the merchant's explicit approval."

## [0:32–0:40] Approval gate
*(Merchant clicks 'Chase unpaid' or the Approve button on the daily check card.)*

> "Tapping Approve opens a single confirmation card in the assistant. Multiple
> reminders, one clear review, one tap. Code enforces that the agent cannot send
> anything alone."

## [0:40–1:35] German: create, approve, send
*(Switch language dropdown to Deutsch: the entire panel changes, and so does the chat. Tap "Tippen und sprechen".)*

**Voice input:**
> "Erstelle eine Rechnung über 30 Euro an kunde@example.com für Gartenarbeit."

*(The German confirmation card appears, showing customer, "Gartenarbeit", 30,00 €, and the calculated 14-day due date.)*

**Voiceover:**
> "Switching to German changes the entire workspace and assistant. Notice the
> confirmation card: PaySay automatically sets a 14-day due date directly through
> PayPal's Invoicing API."

*(Tap "Rechnung erstellen". Real sandbox invoice card appears with its invoice ID; row flashes green in the table.)*

> "A single tap creates the live invoice in the PayPal sandbox through the official
> PayPal MCP server. Sending it is one more tap—again protected by a second confirmation."

*(Tap "Senden" on the invoice card, then tap confirm on the send card.)*

## [1:35–2:05] Timeline (Bryntum)
*(Navigate to Timeline tab or speak.)*

**Voice input:**
> "Zeig mir die Zahlungszeitachse von Anna."

*(The assistant executes `show_timeline`, filters to Anna, and smoothly scrolls to her invoices. Overdue invoices display a distinct red outline.)*

> "The Bryntum collections timeline visualizes invoice lifecycles by customer.
> Overdue invoices are boldly outlined in red, and the 'Today' marker stays clean
> and readable at every screen width. Voice commands filter and scroll the timeline
> on demand."

## [2:05–2:30] Thai: what's unpaid
*(Switch language dropdown to ไทย. Tap mic.)*

**Voice input:**
> "มีใบแจ้งหนี้ไหนที่ยังไม่ได้ชำระบ้าง" *(Which invoices are still unpaid?)*

*(The assistant replies in Thai text, showing unpaid invoice cards with dates formatted in the Buddhist calendar.)*

> "In Thai, dates seamlessly use the Buddhist calendar. Because Amazon Polly has
> no standard Thai voice, PaySay degrades gracefully to localized text so the
> merchant never experiences broken audio."

## [2:30–2:40] How it works
*(Visual architecture diagram on screen.)*

> "Browser speech-to-text goes to a Go backend. An Amazon Bedrock Nova Pro agent
> orchestrates intent via Converse tool use. A strict approval gate in Go code
> prevents unauthorized money movement before calling the official PayPal MCP server
> and Invoicing API."

## [2:40–2:50] Close
*(Return to full desktop screen with https://paysay.onrender.com visible in the browser address bar.)*

> "PaySay: Run your business by voice. Live in the PayPal sandbox at
> paysay.onrender.com."

---

## Recording checklist
- Run in **live sandbox** mode (sidebar shows "PayPal sandbox · live") so the
  invoice IDs are real. Say "PayPal sandbox" only when it is.
- Hosted sandbox data is in EUR, so use the German segment for the create flow.
- Make sure one SENT invoice is more than 3 days old so the daily-check card has something to chase.
- Set `PAYPAL_MERCHANT_NAME` so invoices show a business name.
- Start with **New conversation** and a tidy sandbox (a few invoices in
  different states make the overview look real).
- Rehearse the exact sentences; deliberate phrasing helps speech recognition.
- Chrome, quiet room, browser zoom about 125% so text is legible.
- PayPal's sandbox does not email customers; don't claim an email was received.
- No copyrighted music. Upload to YouTube as public and add the link on Devpost.
