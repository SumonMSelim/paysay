package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// invoicingRESTTimeout bounds each direct Invoicing API call. The MCP tool has
// already created the draft by this point, so a slow due-date update must not
// hold the merchant's confirmation open indefinitely.
const invoicingRESTTimeout = 15 * time.Second

// invoicingError describes a failed Invoicing REST call in a way the caller can
// log without parsing strings. Step is "get" or "put"; Status is the HTTP
// status code, or 0 when the request never got a response (transport or
// encoding failure). It never carries a response body or the token.
type invoicingError struct {
	Step   string
	Status int
	err    error
}

func (e *invoicingError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("paypal: %s invoice returned HTTP %d", e.Step, e.Status)
	}
	return fmt.Sprintf("paypal: %s invoice: %v", e.Step, e.err)
}

func (e *invoicingError) Unwrap() error { return e.err }

// invoicingClient makes the direct PayPal Invoicing v2 REST calls that the MCP
// create_invoice tool cannot: it has no due-date field, so after the draft is
// created we GET the invoice, attach a specified due date, and PUT it back.
//
// It never logs request or response bodies or the access token.
type invoicingClient struct {
	baseURL     string
	accessToken string
	httpClient  *http.Client
}

// newInvoicingClient builds a client for the given environment and token. A nil
// httpClient gets a default with a bounded timeout.
func newInvoicingClient(baseURL, accessToken string, httpClient *http.Client) *invoicingClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: invoicingRESTTimeout}
	}
	return &invoicingClient{
		baseURL:     baseURL,
		accessToken: accessToken,
		httpClient:  httpClient,
	}
}

// setDueDate attaches a specified due date to an existing invoice by GETting the
// full invoice, setting detail.payment_term to a DUE_ON_DATE_SPECIFIED term with
// the given due date, and PUTting the complete invoice back. The update is made
// silently (send_to_recipient=false, send_to_invoicer=false) so attaching the
// due date does not email anyone.
//
// dueDate must be a yyyy-MM-DD string. It returns an error if either call fails;
// callers treat that as "created without a due date", since the draft exists.
//
// Fully update invoice (PUT) and Show invoice details (GET):
// https://developer.paypal.com/api/invoicing/v2/invoices-update
// https://developer.paypal.com/api/invoicing/v2/invoices-get
// payment_term (term_type DUE_ON_DATE_SPECIFIED + due_date):
// https://developer.paypal.com/api/invoicing/v2/definitions/invoice_payment_term
func (c *invoicingClient) setDueDate(ctx context.Context, invoiceID, dueDate string) error {
	if invoiceID == "" {
		return fmt.Errorf("paypal: empty invoice id")
	}

	invoice, err := c.getInvoice(ctx, invoiceID)
	if err != nil {
		return err
	}

	detail, ok := invoice["detail"].(map[string]any)
	if !ok {
		detail = map[string]any{}
		invoice["detail"] = detail
	}
	// term_type DUE_ON_DATE_SPECIFIED tells PayPal to use the explicit due_date.
	detail["payment_term"] = map[string]any{
		"term_type": "DUE_ON_DATE_SPECIFIED",
		"due_date":  dueDate,
	}

	return c.putInvoice(ctx, invoiceID, invoice)
}

// getInvoice performs GET /v2/invoicing/invoices/{id} and returns the decoded
// invoice object. A non-200 status is an error. Response bodies are never logged.
func (c *invoicingClient) getInvoice(ctx context.Context, invoiceID string) (map[string]any, error) {
	endpoint := c.baseURL + "/v2/invoicing/invoices/" + url.PathEscape(invoiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &invoicingError{Step: "get", err: err}
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &invoicingError{Step: "get", err: err}
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, &invoicingError{Step: "get", Status: resp.StatusCode}
	}

	var invoice map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&invoice); err != nil {
		return nil, &invoicingError{Step: "get", err: err}
	}
	return invoice, nil
}

// putInvoice performs PUT /v2/invoicing/invoices/{id} with the complete invoice
// object, suppressing both notification emails via query parameters. A non-200
// status is an error. Request and response bodies are never logged.
func (c *invoicingClient) putInvoice(ctx context.Context, invoiceID string, invoice map[string]any) error {
	endpoint := c.baseURL + "/v2/invoicing/invoices/" + url.PathEscape(invoiceID)
	// send_to_recipient / send_to_invoicer are query parameters on the update
	// operation and both default to true; set them false to update silently.
	q := url.Values{}
	q.Set("send_to_recipient", "false")
	q.Set("send_to_invoicer", "false")
	endpoint += "?" + q.Encode()

	body, err := json.Marshal(invoice)
	if err != nil {
		return &invoicingError{Step: "put", err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return &invoicingError{Step: "put", err: err}
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &invoicingError{Step: "put", err: err}
	}
	defer drainAndClose(resp.Body)

	// Fully update invoice returns 200 OK on success.
	if resp.StatusCode != http.StatusOK {
		return &invoicingError{Step: "put", Status: resp.StatusCode}
	}
	return nil
}

// setHeaders applies the bearer token and Accept header common to both calls.
func (c *invoicingClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Accept", "application/json")
}

// drainAndClose drains and closes a response body so the HTTP connection can be
// reused, without logging its contents.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
