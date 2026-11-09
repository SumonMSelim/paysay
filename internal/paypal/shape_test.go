package paypal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShapeCreateInvoiceArgsFlatToV2(t *testing.T) {
	flat := json.RawMessage(`{
		"recipient_email": "alice@example.com",
		"currency_code": "EUR",
		"items": [
			{"name": "Coffee", "quantity": "2", "unit_amount": "4.50"}
		],
		"note": "Thanks for visiting!"
	}`)

	shaped := ShapeCreateInvoiceArgs(flat, "merchant@example.com")

	var parsed struct {
		Detail struct {
			CurrencyCode string `json:"currency_code"`
			Note         string `json:"note"`
		} `json:"detail"`
		Invoicer struct {
			EmailAddress string `json:"email_address"`
		} `json:"invoicer"`
		PrimaryRecipients []struct {
			BillingInfo struct {
				EmailAddress string `json:"email_address"`
			} `json:"billing_info"`
		} `json:"primary_recipients"`
		Items []struct {
			Name       string `json:"name"`
			Quantity   string `json:"quantity"`
			UnitAmount struct {
				CurrencyCode string `json:"currency_code"`
				Value        string `json:"value"`
			} `json:"unit_amount"`
		} `json:"items"`
	}

	if err := json.Unmarshal(shaped, &parsed); err != nil {
		t.Fatalf("unmarshal shaped JSON: %v", err)
	}

	if parsed.Detail.CurrencyCode != "EUR" {
		t.Errorf("currency = %q, want EUR", parsed.Detail.CurrencyCode)
	}
	if parsed.Invoicer.EmailAddress != "merchant@example.com" {
		t.Errorf("invoicer = %q, want merchant@example.com", parsed.Invoicer.EmailAddress)
	}
	if len(parsed.PrimaryRecipients) != 1 || parsed.PrimaryRecipients[0].BillingInfo.EmailAddress != "alice@example.com" {
		t.Errorf("recipient = %+v", parsed.PrimaryRecipients)
	}
	if len(parsed.Items) != 1 || parsed.Items[0].UnitAmount.Value != "4.50" || parsed.Items[0].UnitAmount.CurrencyCode != "EUR" {
		t.Errorf("items = %+v", parsed.Items)
	}
}

func TestShapeCreateInvoiceArgsAlreadyV2(t *testing.T) {
	v2 := json.RawMessage(`{
		"detail": {"currency_code": "USD"},
		"primary_recipients": [{"billing_info": {"email_address": "bob@example.com"}}]
	}`)

	shaped := ShapeCreateInvoiceArgs(v2, "merchant@example.com")
	if string(shaped) != string(v2) {
		t.Errorf("expected v2 payload to be untouched, got: %s", string(shaped))
	}
}

func TestScrubInvoicerDropsInventedSellerDetails(t *testing.T) {
	raw := json.RawMessage(`{"currency_code":"EUR",
		"invoicer_business_name":"Merchant's Business Name","invoicer_address_line_1":"123 Main St",
		"invoicer_email_address":"merchant@example.com","invoicer_country_code":"US",
		"primary_recipients":[{"billing_info":{"email_address":"buyer@example.com"}}]}`)

	var got map[string]any
	if err := json.Unmarshal(ScrubInvoicer(raw, "", ""), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k := range got {
		if strings.HasPrefix(k, "invoicer_") {
			t.Errorf("invented seller field %q kept", k)
		}
	}
	if got["currency_code"] != "EUR" || got["primary_recipients"] == nil {
		t.Errorf("non-seller fields changed: %v", got)
	}
}

func TestScrubInvoicerUsesConfiguredMerchant(t *testing.T) {
	raw := json.RawMessage(`{"invoicer_email_address":"merchant@example.com","invoicer_city":"Anytown"}`)

	var got map[string]any
	if err := json.Unmarshal(ScrubInvoicer(raw, "shop@sandbox.example", "Lena's Bakery"), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["invoicer_email_address"] != "shop@sandbox.example" || got["invoicer_business_name"] != "Lena's Bakery" {
		t.Errorf("configured merchant not applied: %v", got)
	}
	if _, ok := got["invoicer_city"]; ok {
		t.Error("invented city kept")
	}
}
