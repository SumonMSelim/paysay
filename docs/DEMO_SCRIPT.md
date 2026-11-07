# PaySay — Demo Video Script (under 3 minutes)

Judges may stop watching at 3:00, so the hook and a working end-to-end flow must
land early. Target ~2:45.

Structure: problem (20s) -> what it is (15s) -> live demo in two languages (90s)
-> how it works / tech (30s) -> close (15s).

---

## [0:00–0:20] Hook / problem
*(On camera or voiceover over a shot of a busy small shop / market stall.)*

> "Small merchants lose money every day, not to fraud, but to friction. They're
> too busy serving customers to stop and type out invoices, chase unpaid bills,
> or fight with English-only finance apps. What if they could just *say* it, in
> their own language?"

## [0:20–0:35] What it is
*(Show the PaySay UI, clean, brand and tagline visible.)*

> "This is PaySay: a voice-first AI assistant that lets a merchant run their
> PayPal invoicing by speaking, in German, Thai, or English. Powered by Amazon
> Bedrock and the official PayPal agent toolkit."

## [0:35–1:25] Live demo 1 — German — create + send
*(Select German. Click the mic. Speak clearly.)*

**You say (German):**
> "Erstelle eine Rechnung über 30 Euro für kunde@example.com und sende sie."

*(On screen: transcript appears, then the assistant replies in German. The
"PayPal activity" panel shows `create_invoice` then `send_invoice` with the real
sandbox invoice ID.)*

**Voiceover:**
> "PaySay understood the intent, confirmed the amount, and called two real
> PayPal tools: it created the invoice and sent it, all from one spoken
> sentence. That's a real invoice in the PayPal sandbox."

## [1:25–2:00] Live demo 2 — Thai — read-back query
*(Switch to Thai. Click mic.)*

**You say (Thai):** "มีใบแจ้งหนี้ที่ยังไม่จ่ายไหม" ("Are there any unpaid invoices?")

*(Assistant lists unpaid invoices and replies in Thai.)*

**Voiceover:**
> "Switching languages is instant. Here it answers in Thai, reading back which
> customers still owe money, so the merchant always knows where they stand."

## [2:00–2:10] Safety beat (optional but strong)
*(Say something ambiguous, e.g. just "Erstelle eine Rechnung" with no amount.)*

**Voiceover:**
> "And because this moves real money, PaySay asks before it acts, clarifying
> missing details instead of guessing."

## [2:10–2:40] How it works / tech
*(Simple architecture diagram on screen.)*

> "Under the hood: speech-to-text in the browser, an Amazon Bedrock Nova agent
> that reasons about the request and decides which tools to call, and PayPal's
> official MCP server driving the Invoicing API. The agent treats its own tool
> inputs as untrusted and only calls an allow-listed set of invoice tools."

## [2:40–2:55] Close
> "PaySay: for the millions of small merchants who'd rather talk than type. Run
> your business by voice. Thanks for watching."

---

## Recording tips
- Set the PayPal sandbox token so the activity panel shows a **real invoice
  ID**, this is the single most convincing moment. Only say "PayPal sandbox"
  when it is actually live; otherwise soften the wording.
- Pre-write the exact sentences you will speak and rehearse pronunciation; ASR
  is cleaner with deliberate phrasing.
- Record in Chrome (Web Speech API support), quiet room, browser zoom ~125% so
  text is legible on small screens.
- Keep the activity panel visible during both demos, that is your proof of real
  PayPal calls.
- No copyrighted music. Use silence or a royalty-free track.
- Upload to YouTube, set to public, put the link on the Devpost submission form.
