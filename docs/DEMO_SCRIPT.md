# PaySay: Demo Video Script (under 3 minutes)

Judges may stop at 3:00, so the problem and a working end-to-end flow must land
early. Target about 2:45.

Structure: problem (20s) → the workspace (15s) → German: create and send
(65s) → Thai: what's unpaid (30s) → how it works (30s) → close (15s).

---

## [0:00–0:20] Problem
*(Voiceover over a busy market stall or café counter.)*

> "Small merchants lose money every day, not to fraud, but to friction. They're
> too busy serving customers to type invoices, chase unpaid bills, or fight
> English-only finance apps. What if they could just say it, in their own
> language, and still stay in control?"

## [0:20–0:35] The workspace
*(PaySay open on the Overview, English. Hover the totals and "Needs
attention".)*

> "This is PaySay: a merchant workspace with a voice assistant for PayPal
> invoicing. Outstanding money, what needs attention, and an assistant that
> speaks the merchant's language."

## [0:35–1:40] German: create, confirm, send
*(Switch the language to Deutsch: the whole panel changes. Tap "Tippen und
sprechen".)*

**You say:**
> "Erstelle eine Rechnung über 30 Euro an kunde@example.com für Gartenarbeit."

*(The confirmation card appears: customer, "Gartenarbeit", 30,00 €, total.)*

**Voiceover:**
> "The assistant understood the request and prepared the invoice, but it
> doesn't act on its own. Anything that moves money waits for the merchant."

*(Tap "Rechnung erstellen". The "Rechnung erstellt" card appears with the real
sandbox invoice ID; the new row flashes in the grid.)*

> "That's a real invoice in the PayPal sandbox, created through PayPal's
> official agent toolkit."

*(In the grid or the chat card, tap Send on that invoice, then confirm.)*

> "Sending it is one more tap, again with a confirmation."

## [1:40–2:10] Thai: what's still unpaid?
*(Switch to ไทย. Tap the mic.)*

**You say:** "มีใบแจ้งหนี้ไหนที่ยังไม่ได้ชำระบ้าง" *(Which invoices are still unpaid?)*

*(The reply in Thai, with invoice cards: customer, amount, status, date, and a
Remind button. Dates show in the Buddhist calendar.)*

> "Switching languages is instant, down to the date format. The answer comes
> with the invoices themselves, and a reminder is one tap away."

## [2:10–2:40] How it works
*(Simple diagram: browser → Go → Bedrock Nova → approval → PayPal MCP →
Invoicing API.)*

> "Speech becomes text in the browser. An Amazon Bedrock Nova agent decides
> which PayPal tools to call. Look-ups run immediately; anything that moves
> money becomes a confirmation card, and only the merchant's tap sends it to
> PayPal's MCP server. That rule is enforced in code, not just asked of the
> model."

## [2:40–2:55] Close
> "PaySay: for the millions of small merchants who'd rather talk than type.
> Run your business by voice."

---

## Recording checklist
- Run in **live sandbox** mode (sidebar shows "PayPal sandbox · live") so the
  invoice IDs are real. Say "PayPal sandbox" only when it is.
- Set `PAYPAL_MERCHANT_NAME` so invoices show a business name.
- Start with **New conversation** and a tidy sandbox (a few invoices in
  different states make the overview look real).
- Rehearse the exact sentences; deliberate phrasing helps speech recognition.
- Chrome, quiet room, browser zoom about 125% so text is legible.
- PayPal's sandbox does not email customers; don't claim an email was received.
- No copyrighted music. Upload to YouTube as public and add the link on Devpost.
