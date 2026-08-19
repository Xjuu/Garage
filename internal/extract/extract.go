// Package extract turns an invoice document (PDF or photo) into structured
// fields using the Gemini API with a response schema, so the model must return
// the exact shape we store rather than free prose.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"google.golang.org/genai"
)

// MaxInlineBytes guards the inline-data request limit. Anything larger is
// flagged for manual review instead of failing the whole run.
const MaxInlineBytes = 18 << 20 // 18 MiB

type Client struct {
	genai *genai.Client
	model string
}

// Document is one attachment pulled off an email.
type Document struct {
	Filename string
	MIMEType string
	Data     []byte
}

// Result mirrors the response schema below.
type Result struct {
	Supplier      string  `json:"supplier"`
	InvoiceNumber string  `json:"invoice_number"`
	InvoiceDate   string  `json:"invoice_date"`
	VehicleReg    string  `json:"vehicle_reg"`
	Currency      string  `json:"currency"`
	Netto         float64 `json:"netto"`
	VATAmount     float64 `json:"vat_amount"`
	VATRate       float64 `json:"vat_rate"`
	Brutto        float64 `json:"brutto"`
	IsInvoice     bool    `json:"is_invoice"`
	// IsGeneralStock marks workshop consumables bought for the business at
	// large rather than work on one vehicle.
	IsGeneralStock bool   `json:"is_general_stock"`
	Confidence     string `json:"confidence"`
	Notes          string `json:"notes"`
	Items          []Item `json:"items"`
}

type Item struct {
	PartNumber string  `json:"part_number"`
	Desc       string  `json:"description"`
	VehicleReg string  `json:"vehicle_reg"`
	Quantity   float64 `json:"quantity"`
	UnitPrice  float64 `json:"unit_price"`
	Netto      float64 `json:"netto"`
	VATAmount  float64 `json:"vat_amount"`
	VATRate    float64 `json:"vat_rate"`
	Brutto     float64 `json:"brutto"`
}

func New(ctx context.Context, apiKey, model string) (*Client, error) {
	c, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Client{genai: c, model: model}, nil
}

// ListModels returns the models this API key can call for content generation,
// so a wrong GEMINI_MODEL is a one-command diagnosis rather than a guess.
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	c, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, err
	}
	var out []string
	for m, err := range c.Models.All(ctx) {
		if err != nil {
			return out, err
		}
		for _, action := range m.SupportedActions {
			if action == "generateContent" {
				out = append(out, strings.TrimPrefix(m.Name, "models/"))
				break
			}
		}
	}
	return out, nil
}

// Guidance is the learned context injected into each extraction: free-text
// hints keyed by supplier, plus fully worked examples. Both come from the
// database, so accuracy improves as the operator corrects things without any
// code change.
type Guidance struct {
	// Hints maps a supplier name to advice about that supplier's layout.
	Hints map[string]string
	// Examples are (document description -> correct JSON) pairs.
	Examples []WorkedExample
}

type WorkedExample struct {
	Supplier  string
	Filename  string
	TruthJSON string
}

// maxExamplesInPrompt caps how many worked examples ride along on every call.
// Each one costs tokens on every invoice, so the newest few earn their place
// and the rest stay in the database for the eval run.
const maxExamplesInPrompt = 3

// build renders the guidance as prompt text, or "" when there is none.
func (g *Guidance) build() string {
	if g == nil {
		return ""
	}
	var b strings.Builder

	if len(g.Hints) > 0 {
		b.WriteString("\n\nSUPPLIER-SPECIFIC GUIDANCE. If the document is from one of these " +
			"suppliers, follow the note for it. Ignore the rest.\n")
		// Sorted so the prompt is stable between runs and stays cacheable.
		names := make([]string, 0, len(g.Hints))
		for name := range g.Hints {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "- %s: %s\n", name, g.Hints[name])
		}
	}

	if len(g.Examples) > 0 {
		b.WriteString("\n\nWORKED EXAMPLES. These are real invoices with the output a human " +
			"confirmed is correct. Match this style of reading — especially which field on " +
			"the page maps to which output field.\n")
		examples := g.Examples
		if len(examples) > maxExamplesInPrompt {
			examples = examples[:maxExamplesInPrompt]
		}
		for i, ex := range examples {
			fmt.Fprintf(&b, "\nExample %d — %s (%s):\n%s\n", i+1, ex.Supplier, ex.Filename, ex.TruthJSON)
		}
	}
	return b.String()
}

const systemPrompt = `You extract data from supplier invoices for Goldstar Diamond Cars, a UK taxi company in Peterborough, and its associated businesses (MFS Motorgroup, and work carried out for external clients). They maintain a fleet of vehicles.

Rules:
- Read every figure off the document. NEVER assume or compute a VAT rate that is not printed; UK invoices are usually 20% but may be 5% or 0%, so report what the document actually states.
- netto = total excluding VAT. brutto = total including VAT. vat_amount = the VAT charged. If only two of the three are printed, derive the third and say so in notes.
- vehicle_reg is a UK vehicle registration plate (e.g. "AB12 CDE", "M8 TXI"). Invoices often name the vehicle the parts or work were for. Put it at invoice level if it applies to the whole invoice, and on a line item if different lines name different vehicles. Use "" when absent — never invent one.
- NOT every invoice is for a particular vehicle. Workshop and general stock purchases — WD-40, engine oil in bulk, brake cleaner, screenwash, gloves, rags, consumables, tools, workshop equipment, office supplies, fuel cards, subscriptions — are bought for the business as a whole. Set is_general_stock true for those and leave vehicle_reg "". Do not guess a registration to fill the gap.
- Set is_general_stock false when the invoice names a vehicle, or clearly concerns work on one (a specific car's parts, an MOT, a repair job).
- If an invoice genuinely names no vehicle and is not general stock either, leave is_general_stock false and vehicle_reg "" — it will be flagged for a human to look at.
- part_number is the supplier's part/SKU code for a line. Labour, call-out and delivery lines have no part number; use "" and still record the line.
- Record EVERY line item, including labour and delivery.
- invoice_date is the date of purchase in ISO YYYY-MM-DD form.
- currency is an ISO code such as GBP.
- Set is_invoice false if the document is not an invoice or receipt (a statement, advert or delivery note).
- Set confidence to "low" if the document is blurred, cropped or ambiguous.
- Use "" for missing text and 0 for missing numbers. Do not guess.`

func responseSchema() *genai.Schema {
	num := &genai.Schema{Type: genai.TypeNumber}
	str := &genai.Schema{Type: genai.TypeString}

	item := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"part_number": {Type: genai.TypeString, Description: "Supplier part/SKU code, or empty for labour and delivery lines"},
			"description": str,
			"vehicle_reg": {Type: genai.TypeString, Description: "UK plate for this line if it differs from the invoice, else empty"},
			"quantity":    num,
			"unit_price":  num,
			"netto":       num,
			"vat_amount":  num,
			"vat_rate":    {Type: genai.TypeNumber, Description: "Percentage as printed, e.g. 20"},
			"brutto":      num,
		},
		Required: []string{"part_number", "description", "vehicle_reg", "quantity", "unit_price", "netto", "vat_amount", "vat_rate", "brutto"},
	}

	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"supplier":         str,
			"invoice_number":   str,
			"invoice_date":     {Type: genai.TypeString, Description: "Date of purchase, ISO YYYY-MM-DD"},
			"vehicle_reg":      {Type: genai.TypeString, Description: "UK vehicle registration plate, empty if absent"},
			"currency":         {Type: genai.TypeString, Description: "ISO code, e.g. GBP"},
			"netto":            {Type: genai.TypeNumber, Description: "Total excluding VAT"},
			"vat_amount":       {Type: genai.TypeNumber, Description: "VAT charged, as printed"},
			"vat_rate":         {Type: genai.TypeNumber, Description: "VAT percentage as printed, e.g. 20"},
			"brutto":           {Type: genai.TypeNumber, Description: "Total including VAT"},
			"is_invoice":       {Type: genai.TypeBoolean},
			"is_general_stock": {Type: genai.TypeBoolean, Description: "True for workshop consumables and general stock not tied to one vehicle"},
			"confidence":       {Type: genai.TypeString, Enum: []string{"high", "medium", "low"}},
			"notes":            {Type: genai.TypeString, Description: "Anything derived rather than read, or anything odd"},
			"items":            {Type: genai.TypeArray, Items: item},
		},
		Required: []string{"supplier", "invoice_number", "invoice_date", "vehicle_reg", "currency",
			"netto", "vat_amount", "vat_rate", "brutto", "is_invoice", "is_general_stock",
			"confidence", "notes", "items"},
		PropertyOrdering: []string{"is_invoice", "is_general_stock", "supplier", "invoice_number",
			"invoice_date", "vehicle_reg", "currency", "netto", "vat_amount", "vat_rate", "brutto",
			"confidence", "notes", "items"},
	}
}

// Extract sends one document to Gemini and returns the parsed result plus the
// raw JSON, which we keep so a bad extraction can be re-audited later.
func (c *Client) Extract(ctx context.Context, doc Document, g *Guidance) (*Result, string, error) {
	if len(doc.Data) > MaxInlineBytes {
		return nil, "", fmt.Errorf("%s is %d bytes, over the %d inline limit", doc.Filename, len(doc.Data), MaxInlineBytes)
	}

	contents := []*genai.Content{genai.NewContentFromParts([]*genai.Part{
		genai.NewPartFromBytes(doc.Data, doc.MIMEType),
		genai.NewPartFromText("Extract the invoice data from this document (filename: " + doc.Filename + ")."),
	}, genai.RoleUser)}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt+g.build(), genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    responseSchema(),
		Temperature:       genai.Ptr[float32](0),
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := c.genai.Models.GenerateContent(ctx, c.model, contents, cfg)
		if err != nil {
			lastErr = err
			// Transient server/rate errors are worth a back-off; anything else is not.
			if !isRetryable(err) || attempt == 3 {
				break
			}
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
			continue
		}

		raw := strings.TrimSpace(resp.Text())
		if raw == "" {
			lastErr = fmt.Errorf("empty response from %s", c.model)
			continue
		}
		var out Result
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, raw, fmt.Errorf("parse model JSON: %w", err)
		}
		out.InvoiceDate = normalizeDate(out.InvoiceDate)
		out.VehicleReg = normalizeReg(out.VehicleReg)
		for i := range out.Items {
			out.Items[i].VehicleReg = normalizeReg(out.Items[i].VehicleReg)
			out.Items[i].PartNumber = strings.TrimSpace(out.Items[i].PartNumber)
		}
		return &out, raw, nil
	}
	return nil, "", fmt.Errorf("gemini extraction failed: %w", lastErr)
}

func isRetryable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, sig := range []string{"429", "500", "502", "503", "504", "rate limit", "unavailable", "deadline", "timeout", "overloaded"} {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

// normalizeDate accepts the handful of formats suppliers print and returns ISO,
// leaving anything unrecognised untouched for a human to inspect.
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{"2006-01-02", "02/01/2006", "02-01-2006", "02.01.2006",
		"2 January 2006", "02 January 2006", "2 Jan 2006", "02 Jan 2006", "Jan 2, 2006", "01/02/2006"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}

// normalizeReg upper-cases and strips separators so the same plate written
// "ab12cde" and "AB12 CDE" groups together in the spreadsheet.
func normalizeReg(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", ".", "", "/", "", ",", "").Replace(s)
	// A placeholder such as "-" or "N/A" is not a registration. Every UK plate
	// has at least one digit, so a value without one is discarded rather than
	// stored as if it were a vehicle.
	if !strings.ContainsFunc(s, unicode.IsDigit) {
		return ""
	}
	return s
}
