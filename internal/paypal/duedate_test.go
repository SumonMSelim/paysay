package paypal

import (
	"encoding/json"
	"testing"
	"time"
)

// withClock pins invoiceClock to a fixed instant for the duration of the test.
func withClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := invoiceClock
	invoiceClock = func() time.Time { return at }
	t.Cleanup(func() { invoiceClock = prev })
}

// fixedNow is the reference "today" used across the date tests.
var fixedNow = time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)

func resolvedDates(t *testing.T, raw string) (invoiceDate, dueDate string) {
	t.Helper()
	out := applyInvoiceDates(json.RawMessage(raw))
	var got struct {
		InvoiceDate string `json:"invoice_date"`
		DueDate     string `json:"due_date"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal resolved args: %v", err)
	}
	return got.InvoiceDate, got.DueDate
}

func TestApplyInvoiceDatesDefaultsTo14Days(t *testing.T) {
	withClock(t, fixedNow)
	invDate, dueDate := resolvedDates(t, `{"currency_code":"EUR","items":[]}`)
	if invDate != "2026-03-01" {
		t.Errorf("invoice_date = %q, want 2026-03-01 (today)", invDate)
	}
	// 2026-03-01 + 14 days = 2026-03-15.
	if dueDate != "2026-03-15" {
		t.Errorf("due_date = %q, want 2026-03-15 (today + %d days)", dueDate, DefaultDueDateDays)
	}
}

func TestApplyInvoiceDatesHonoursExplicitValidDueDate(t *testing.T) {
	withClock(t, fixedNow)
	invDate, dueDate := resolvedDates(t, `{"currency_code":"EUR","due_date":"2026-03-20"}`)
	if invDate != "2026-03-01" {
		t.Errorf("invoice_date = %q, want 2026-03-01", invDate)
	}
	if dueDate != "2026-03-20" {
		t.Errorf("due_date = %q, want 2026-03-20 (explicit value honoured)", dueDate)
	}
}

func TestApplyInvoiceDatesHonoursExplicitInvoiceDate(t *testing.T) {
	withClock(t, fixedNow)
	// An explicit invoice_date with no due date: default runs from the invoice
	// date, not today.
	invDate, dueDate := resolvedDates(t, `{"invoice_date":"2026-03-10"}`)
	if invDate != "2026-03-10" {
		t.Errorf("invoice_date = %q, want 2026-03-10", invDate)
	}
	if dueDate != "2026-03-24" {
		t.Errorf("due_date = %q, want 2026-03-24 (invoice_date + 14 days)", dueDate)
	}
}

func TestApplyInvoiceDatesFallsBackOnInvalidValues(t *testing.T) {
	withClock(t, fixedNow)
	cases := []struct {
		name string
		raw  string
	}{
		{"not a date", `{"due_date":"next friday"}`},
		{"wrong format", `{"due_date":"01/03/2026"}`},
		{"in the past", `{"due_date":"2026-02-01"}`},
		{"more than a year ahead", `{"due_date":"2027-06-01"}`},
		{"not a string", `{"due_date":123}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, dueDate := resolvedDates(t, c.raw)
			if dueDate != "2026-03-15" {
				t.Errorf("due_date = %q, want 2026-03-15 (14-day fallback)", dueDate)
			}
		})
	}
}

func TestApplyInvoiceDatesAcceptsExactlyOneYearAhead(t *testing.T) {
	withClock(t, fixedNow)
	// Exactly one year ahead is within the limit and must be honoured.
	_, dueDate := resolvedDates(t, `{"due_date":"2027-03-01"}`)
	if dueDate != "2027-03-01" {
		t.Errorf("due_date = %q, want 2027-03-01 (exactly one year ahead is valid)", dueDate)
	}
}

func TestApplyInvoiceDatesIsIdempotent(t *testing.T) {
	withClock(t, fixedNow)
	once := applyInvoiceDates(json.RawMessage(`{"currency_code":"EUR"}`))
	twice := applyInvoiceDates(once)
	if string(once) != string(twice) {
		t.Errorf("not idempotent:\n once:  %s\n twice: %s", once, twice)
	}
}

func TestApplyInvoiceDatesLeavesNestedPayloadUntouched(t *testing.T) {
	withClock(t, fixedNow)
	nested := json.RawMessage(`{"detail":{"currency_code":"USD"}}`)
	if got := applyInvoiceDates(nested); string(got) != string(nested) {
		t.Errorf("nested payload changed: %s", got)
	}
}

func TestShapeCreateInvoiceArgsCarriesResolvedDates(t *testing.T) {
	withClock(t, fixedNow)
	flat := json.RawMessage(`{
		"recipient_email": "alice@example.com",
		"currency_code": "EUR",
		"due_date": "2026-03-20",
		"items": [{"name": "Coffee", "quantity": "2", "unit_amount": "4.50"}]
	}`)

	shaped := ShapeCreateInvoiceArgs(flat, "merchant@example.com")
	var parsed struct {
		Detail struct {
			InvoiceDate string `json:"invoice_date"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(shaped, &parsed); err != nil {
		t.Fatalf("unmarshal shaped: %v", err)
	}
	// The MCP create_invoice tool accepts invoice_date as a yyyy-MM-DD string;
	// confirm the shaped payload carries a date in that exact format.
	if _, err := time.Parse(dueDateLayout, parsed.Detail.InvoiceDate); err != nil {
		t.Errorf("detail.invoice_date = %q, not a yyyy-MM-DD date", parsed.Detail.InvoiceDate)
	}
	if parsed.Detail.InvoiceDate != "2026-03-01" {
		t.Errorf("detail.invoice_date = %q, want 2026-03-01", parsed.Detail.InvoiceDate)
	}
}
