package paypal

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// DefaultDueDateDays is how many days after the invoice date a new invoice is
// due when the merchant does not name a due date. The merchant chose 14 days.
// PayPal has no 14-day payment term, so this is applied as a specified due date
// rather than a term type.
const DefaultDueDateDays = 14

// maxDueDateAhead bounds how far in the future a model-supplied due date may be.
// A value further out than this is treated as a mistake and falls back to the
// default, so a hallucinated date cannot push an invoice years away.
const maxDueDateAhead = 365 * 24 * time.Hour

// dueDateLayout is the yyyy-MM-DD format PayPal's invoice_date field uses and
// the format we store the due date in for the mock and the confirmation card.
const dueDateLayout = "2006-01-02"

// invoiceClock supplies the current time for date resolution. It is a package
// variable so tests can pin "now"; production uses the real UTC clock.
var invoiceClock = func() time.Time { return time.Now().UTC() }

// ApplyCreateInvoiceDates fills in the invoice_date and due_date on flat
// create_invoice arguments so the merchant's confirmation card shows the exact
// dates the invoice will use. It is applied before an action is proposed for
// approval, and is idempotent: running it again on its own output is a no-op.
// It never fails; a bad model-supplied value falls back to a safe default.
func ApplyCreateInvoiceDates(raw json.RawMessage) json.RawMessage {
	return applyInvoiceDates(raw)
}

// applyInvoiceDates fills in the invoice_date and due_date on flat create_invoice
// arguments. It never fails: a missing or invalid model-supplied value falls
// back to a safe default so a bad value can never break invoice creation.
//
//   - invoice_date: the model's value if it is a valid yyyy-MM-DD date,
//     otherwise today (UTC).
//   - due_date: the model's value if it is valid (a yyyy-MM-DD date that is not
//     in the past and at most one year ahead), otherwise the invoice date plus
//     DefaultDueDateDays.
//
// Both fields are written back as yyyy-MM-DD strings. The arguments are assumed
// flat (the conversational shape); a v2/nested payload is returned unchanged.
func applyInvoiceDates(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return raw
	}
	// A nested v2 payload carries its dates under detail; leave it untouched.
	if _, nested := args["detail"]; nested {
		return raw
	}

	now := invoiceClock()
	invoiceDate := resolveInvoiceDate(args["invoice_date"], now)
	dueDate := resolveDueDate(args["due_date"], invoiceDate, now)

	args["invoice_date"] = invoiceDate.Format(dueDateLayout)
	args["due_date"] = dueDate.Format(dueDateLayout)

	out, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return out
}

// resolveInvoiceDate returns the model-supplied invoice date if it parses as a
// yyyy-MM-DD date, otherwise today.
func resolveInvoiceDate(v any, now time.Time) time.Time {
	if d, ok := parseDate(v); ok {
		return d
	}
	return truncateToDay(now)
}

// resolveDueDate returns the model-supplied due date when it is valid, otherwise
// the invoice date plus the default term. A valid due date is a yyyy-MM-DD date
// that is not before today and at most one year ahead of today.
func resolveDueDate(v any, invoiceDate, now time.Time) time.Time {
	today := truncateToDay(now)
	fallback := invoiceDate.AddDate(0, 0, DefaultDueDateDays)
	d, ok := parseDate(v)
	if !ok {
		return fallback
	}
	if d.Before(today) {
		return fallback
	}
	if d.After(today.Add(maxDueDateAhead)) {
		return fallback
	}
	return d
}

// parseDate reads a yyyy-MM-DD date from a JSON value that the model may have
// produced as a string. It returns the date (at UTC midnight) and whether it
// parsed.
func parseDate(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	d, err := time.Parse(dueDateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// resolvedDueDate returns the due date (yyyy-MM-DD) that applyInvoiceDates would
// set for the given flat create_invoice arguments, without reshaping them. It is
// used by the live path to know which due date to attach via the REST API.
func resolvedDueDate(raw json.RawMessage) string {
	resolved := applyInvoiceDates(raw)
	var got struct {
		DueDate string `json:"due_date"`
	}
	if err := json.Unmarshal(resolved, &got); err != nil {
		return ""
	}
	return got.DueDate
}

// annotateDueDate adds due_date and due_date_set to a create_invoice tool
// output so the reply and confirmation card can state whether the invoice ended
// up with the due date. When the output is not a JSON object it is wrapped in
// one under "result" so the annotation is always machine-readable. due_date is
// always the date the app intended to set; due_date_set reports whether the
// Invoicing API call actually applied it.
func annotateDueDate(output, dueDate string, set bool) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err != nil || obj == nil {
		obj = map[string]any{"result": output}
	}
	obj["due_date"] = dueDate
	obj["due_date_set"] = set
	if !set {
		obj["due_date_note"] = "The invoice was created but its due date could not be set, so it has no due date yet."
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return output
	}
	return string(b)
}

// invoiceIDPattern matches PayPal invoice IDs (e.g. INV2-ABCD-...) in create
// output, so the live path can find the new invoice to update. It mirrors the
// pattern the server uses to record created invoices.
var invoiceIDPattern = regexp.MustCompile(`INV2-[A-Z0-9-]+`)

// truncateToDay drops the time-of-day so date comparisons work on calendar days.
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
