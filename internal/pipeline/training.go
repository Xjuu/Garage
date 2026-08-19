package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goldstar/internal/config"
	"goldstar/internal/extract"
	"goldstar/internal/store"
)

// LoadGuidance assembles the supplier hints and worked examples that are sent
// with every extraction. A failure here is never fatal: extraction without
// guidance is exactly the original behaviour, so we degrade rather than stop.
func LoadGuidance(db *store.Store, logf LogFunc) *extract.Guidance {
	g := &extract.Guidance{}

	hints, err := db.AllHints()
	if err != nil {
		logf.printf("could not load supplier hints: %v", err)
	} else {
		g.Hints = hints
	}

	examples, err := db.ReadyExamples()
	if err != nil {
		logf.printf("could not load examples: %v", err)
		return g
	}
	if g.Hints == nil {
		g.Hints = map[string]string{}
	}
	for _, e := range examples {
		g.Examples = append(g.Examples, extract.WorkedExample{
			Supplier: e.Supplier, Filename: e.Filename, TruthJSON: e.TruthJSON,
		})
		// Where-to-look notes typed on the Training page become layout guidance
		// for that supplier, merged after any hand-written hint.
		if layout := layoutNote(e.TruthJSON); layout != "" && e.Supplier != "" {
			if existing := g.Hints[e.Supplier]; existing != "" {
				g.Hints[e.Supplier] = existing + " " + layout
			} else {
				g.Hints[e.Supplier] = layout
			}
		}
	}
	if n := len(g.Hints); n > 0 {
		logf.printf("using %d supplier hint(s)", n)
	}
	if n := len(g.Examples); n > 0 {
		logf.printf("using %d worked example(s)", n)
	}
	return g
}

// ScanExamples registers every supported file sitting in the examples folder.
// It is safe to run repeatedly; files are keyed by content hash.
func ScanExamples(cfg *config.Config, db *store.Store, logf LogFunc) (added, seen int, err error) {
	dir := cfg.ExamplesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		mimeType := mimeFromExt(entry.Name())
		if mimeType == "" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			logf.printf("%s: %v", entry.Name(), err)
			continue
		}
		seen++

		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		_, isNew, err := db.AddExample(sha, entry.Name(), path, mimeType)
		if err != nil {
			logf.printf("%s: %v", entry.Name(), err)
			continue
		}
		if isNew {
			added++
			logf.printf("registered example %s — needs its correct values entering", entry.Name())
		}
	}
	return added, seen, nil
}

// fieldLabels name each field the way an operator would describe it, so the
// generated guidance reads as instructions rather than as schema keys.
var fieldLabels = map[string]string{
	"supplier":       "the supplier name",
	"invoice_number": "the invoice number",
	"invoice_date":   "the invoice date",
	"vehicle_reg":    "the vehicle registration",
	"netto":          "the total excluding VAT",
	"vat_amount":     "the VAT amount",
	"vat_rate":       "the VAT rate",
	"brutto":         "the total including VAT",
	"items":          "the line items table",
}

// layoutOrder keeps the generated sentence stable between runs, which matters
// because an unstable prompt defeats caching.
var layoutOrder = []string{
	"supplier", "invoice_number", "invoice_date", "vehicle_reg",
	"netto", "vat_amount", "vat_rate", "brutto", "items",
}

// layoutNote turns the `sections` map recorded against an example into a
// sentence telling the model where to look on that supplier's invoices.
func layoutNote(truthJSON string) string {
	var parsed struct {
		Sections map[string]string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(truthJSON), &parsed); err != nil || len(parsed.Sections) == 0 {
		return ""
	}
	var parts []string
	for _, key := range layoutOrder {
		where := strings.TrimSpace(parsed.Sections[key])
		if where == "" {
			continue
		}
		label := fieldLabels[key]
		if label == "" {
			label = key
		}
		parts = append(parts, fmt.Sprintf("%s is found at %s", label, where))
	}
	if len(parts) == 0 {
		return ""
	}
	return "On this supplier's invoices, " + strings.Join(parts, "; ") + "."
}

// EvalResult is the per-example outcome of a regression run.
type EvalResult struct {
	ExampleID int64             `json:"example_id"`
	Filename  string            `json:"filename"`
	Supplier  string            `json:"supplier"`
	FieldsOK  int               `json:"fields_ok"`
	FieldsAll int               `json:"fields_all"`
	Mismatch  map[string]string `json:"mismatch"`
	Error     string            `json:"error,omitempty"`
}

// Eval re-extracts every completed example and compares the result against the
// values a human confirmed. This is what makes prompt and model changes safe:
// without it, a tweak that quietly degrades accuracy is invisible until a VAT
// return is wrong.
func Eval(ctx context.Context, cfg *config.Config, db *store.Store, logf LogFunc) ([]EvalResult, error) {
	if err := cfg.RequireGemini(); err != nil {
		return nil, err
	}
	examples, err := db.ReadyExamples()
	if err != nil {
		return nil, err
	}
	if len(examples) == 0 {
		return nil, fmt.Errorf("no completed examples yet — add files to %s and fill in their correct values on the Training page", cfg.ExamplesDir())
	}

	ex, err := extract.New(ctx, cfg.GeminiKey, cfg.GeminiModel)
	if err != nil {
		return nil, err
	}

	runID, err := db.StartEvalRun(cfg.GeminiModel)
	if err != nil {
		return nil, err
	}

	// Evaluate with the hints and the where-to-look notes, but drop the worked
	// examples: those carry the answers, so leaving them in would let each
	// example grade itself and report a near-perfect score every time. The
	// location notes say only where to look, which is exactly what production
	// extraction gets, so keeping them makes the score representative.
	guidance := LoadGuidance(db, nil)
	guidance.Examples = nil

	results := make([]EvalResult, 0, len(examples))
	totalOK, totalAll := 0, 0

	for _, e := range examples {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		r := EvalResult{ExampleID: e.ID, Filename: e.Filename, Supplier: e.Supplier,
			Mismatch: map[string]string{}}

		data, err := os.ReadFile(e.SourceFile)
		if err != nil {
			r.Error = err.Error()
			results = append(results, r)
			logf.printf("%s: %v", e.Filename, err)
			continue
		}

		got, _, err := ex.Extract(ctx, extract.Document{
			Filename: e.Filename, MIMEType: e.MIMEType, Data: data,
		}, guidance)
		if err != nil {
			r.Error = err.Error()
			results = append(results, r)
			logf.printf("%s: %v", e.Filename, err)
			continue
		}

		var truth map[string]any
		if err := json.Unmarshal([]byte(e.TruthJSON), &truth); err != nil {
			r.Error = "stored ground truth is not valid JSON"
			results = append(results, r)
			continue
		}

		r.FieldsOK, r.FieldsAll, r.Mismatch = compare(truth, got)
		totalOK += r.FieldsOK
		totalAll += r.FieldsAll
		results = append(results, r)

		logf.printf("%-28s %d/%d fields%s", trim(e.Filename, 28), r.FieldsOK, r.FieldsAll,
			mismatchSuffix(r.Mismatch))
	}

	detail, _ := json.Marshal(results)
	if err := db.FinishEvalRun(runID, len(examples), totalOK, totalAll, string(detail)); err != nil {
		logf.printf("could not record eval run: %v", err)
	}
	if totalAll > 0 {
		logf.printf("overall accuracy %.1f%% (%d/%d fields across %d example(s))",
			float64(totalOK)/float64(totalAll)*100, totalOK, totalAll, len(examples))
	}
	return results, nil
}

// compare checks only the fields present in the ground truth, so a partially
// filled example still contributes rather than being scored as mostly wrong.
func compare(truth map[string]any, got *extract.Result) (ok, all int, mismatch map[string]string) {
	mismatch = map[string]string{}

	actual := map[string]any{
		"supplier":       got.Supplier,
		"invoice_number": got.InvoiceNumber,
		"invoice_date":   got.InvoiceDate,
		"vehicle_reg":    got.VehicleReg,
		"currency":       got.Currency,
		"netto":          got.Netto,
		"vat_amount":     got.VATAmount,
		"vat_rate":       got.VATRate,
		"brutto":         got.Brutto,
	}

	keys := make([]string, 0, len(truth))
	for k := range truth {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		want := truth[key]
		if key == "items" {
			o, a, m := compareItems(want, got.Items)
			ok += o
			all += a
			for k, v := range m {
				mismatch[k] = v
			}
			continue
		}
		have, known := actual[key]
		if !known {
			continue
		}
		all++
		if equalField(want, have) {
			ok++
		} else {
			mismatch[key] = fmt.Sprintf("expected %v, got %v", want, have)
		}
	}
	return ok, all, mismatch
}

// compareItems matches line items by part number where possible, since the
// model may legitimately order lines differently from the typed truth.
func compareItems(want any, got []extract.Item) (ok, all int, mismatch map[string]string) {
	mismatch = map[string]string{}
	list, isList := want.([]any)
	if !isList {
		return 0, 0, mismatch
	}

	byPart := map[string]extract.Item{}
	for _, it := range got {
		if it.PartNumber != "" {
			byPart[strings.ToUpper(it.PartNumber)] = it
		}
	}

	all++
	if len(list) == len(got) {
		ok++
	} else {
		mismatch["items.count"] = fmt.Sprintf("expected %d line(s), got %d", len(list), len(got))
	}

	for i, raw := range list {
		obj, isObj := raw.(map[string]any)
		if !isObj {
			continue
		}
		part, _ := obj["part_number"].(string)
		if part == "" {
			continue
		}
		all++
		match, found := byPart[strings.ToUpper(part)]
		if !found {
			mismatch[fmt.Sprintf("items[%d].part_number", i)] = fmt.Sprintf("%q not found in output", part)
			continue
		}
		ok++

		for field, have := range map[string]any{
			"unit_price": match.UnitPrice,
			"quantity":   match.Quantity,
			"netto":      match.Netto,
			"vat_amount": match.VATAmount,
		} {
			expected, present := obj[field]
			if !present {
				continue
			}
			all++
			if equalField(expected, have) {
				ok++
			} else {
				mismatch[fmt.Sprintf("items[%s].%s", part, field)] =
					fmt.Sprintf("expected %v, got %v", expected, have)
			}
		}
	}
	return ok, all, mismatch
}

// equalField compares loosely: money to the penny, text case-insensitively
// with whitespace collapsed. A supplier typed "Ltd" versus "LTD" is not an
// extraction failure.
func equalField(want, have any) bool {
	switch w := want.(type) {
	case float64:
		h, isNum := toFloat(have)
		return isNum && math.Abs(w-h) < 0.005
	case string:
		hs, isStr := have.(string)
		if !isStr {
			return false
		}
		return normalizeText(w) == normalizeText(hs)
	case bool:
		h, isBool := have.(bool)
		return isBool && w == h
	case nil:
		return have == nil || have == "" || have == 0.0
	}
	return fmt.Sprint(want) == fmt.Sprint(have)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func normalizeText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func mismatchSuffix(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 3 {
		keys = append(keys[:3], "…")
	}
	return "  wrong: " + strings.Join(keys, ", ")
}
