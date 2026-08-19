package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"goldstar/internal/store"
)

// File is one workbook on disk, as shown in the exports list.
type File struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytes"`
	Created  string  `json:"created"`
	Label    string  `json:"label"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	Invoices int     `json:"invoices"`
	Items    int     `json:"items"`
	Brutto   float64 `json:"brutto"`
	// Stale marks a workbook whose manifest is missing — usually one written by
	// an older version. It is still listed and downloadable; only the summary
	// counts are unknown, and saying so is better than showing zeros as if they
	// were real.
	Stale bool `json:"stale"`
}

// manifest is the sidecar written next to each workbook. Reading a 500-row
// .xlsx back just to show "how many invoices" costs far more than reading a
// few hundred bytes of JSON, and the answer never changes once written.
type manifest struct {
	Label    string    `json:"label"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Invoices int       `json:"invoices"`
	Items    int       `json:"items"`
	Brutto   float64   `json:"brutto"`
	Created  time.Time `json:"created"`
}

// Catalogue lists generated workbooks, caching the result until the folder
// changes. Every visit to the Exports tab would otherwise stat and parse the
// whole directory.
type Catalogue struct {
	dir string

	mu       sync.Mutex
	cached   []File
	cacheKey string
	hits     int
	misses   int
}

func NewCatalogue(dir string) *Catalogue { return &Catalogue{dir: dir} }

// Stats reports cache effectiveness, shown on the Exports tab so the caching
// is observable rather than a claim.
func (c *Catalogue) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// List returns the workbooks, newest first.
func (c *Catalogue) List() ([]File, error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, err
	}

	// The key covers name, size and modification time of every workbook, so a
	// regenerated file with the same name still invalidates the cache.
	var b strings.Builder
	type pair struct {
		name string
		info os.FileInfo
	}
	var found []pair
	for _, e := range entries {
		if e.IsDir() || !isExport(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, pair{e.Name(), info})
		fmt.Fprintf(&b, "%s:%d:%d|", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	key := b.String()

	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.cacheKey && c.cached != nil {
		c.hits++
		out := make([]File, len(c.cached))
		copy(out, c.cached)
		return out, nil
	}
	c.misses++

	files := make([]File, 0, len(found))
	for _, p := range found {
		full := filepath.Join(c.dir, p.name)
		f := File{
			Name:    p.name,
			Path:    full,
			Bytes:   p.info.Size(),
			Created: p.info.ModTime().Format(time.RFC3339),
			Stale:   true,
		}
		if strings.HasSuffix(strings.ToLower(p.name), ".csv") {
			// CSVs carry no sidecar; count their rows directly, which is cheap
			// and keeps them from showing as "unknown" beside the workbooks.
			if n, err := countCSVRows(full); err == nil {
				f.Items, f.Label, f.Stale = n, "CSV export", false
			}
		}
		if m, err := readManifest(manifestPath(full)); err == nil {
			f.Label, f.From, f.To = m.Label, m.From, m.To
			f.Invoices, f.Items, f.Brutto = m.Invoices, m.Items, m.Brutto
			f.Stale = false
			if !m.Created.IsZero() {
				f.Created = m.Created.Format(time.RFC3339)
			}
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Created > files[j].Created })

	c.cached, c.cacheKey = files, key
	out := make([]File, len(files))
	copy(out, files)
	return out, nil
}

// Invalidate drops the cached listing. Called after writing or deleting a
// workbook so the next List reflects it immediately rather than waiting for a
// timestamp comparison that could tie at one-second resolution.
func (c *Catalogue) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cacheKey = ""
	c.cached = nil
}

// isExport recognises the two formats the app writes.
func isExport(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".xlsx") || strings.HasSuffix(l, ".csv")
}

func manifestPath(xlsx string) string {
	return strings.TrimSuffix(xlsx, filepath.Ext(xlsx)) + ".json"
}

// countCSVRows counts data rows without loading the file into memory.
func countCSVRows(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	n := 0
	for {
		if _, err := r.Read(); err == io.EOF {
			break
		} else if err != nil {
			return 0, err
		}
		n++
	}
	if n > 0 {
		n-- // discount the header
	}
	return n, nil
}

func readManifest(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// WriteWithManifest renders the workbook and records its summary alongside.
func WriteWithManifest(path, label, from, to string, invoices []store.Invoice) (string, error) {
	if _, err := Write(path, invoices); err != nil {
		return "", err
	}
	m := manifest{
		Label: label, From: from, To: to,
		Invoices: len(invoices), Created: time.Now(),
	}
	for _, inv := range invoices {
		m.Items += len(inv.Items)
		m.Brutto += inv.Brutto
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	// A missing manifest is recoverable — the workbook still lists, just
	// without its counts — so this failure must not lose the file itself.
	if err := os.WriteFile(manifestPath(path), data, 0o600); err != nil {
		return path, nil
	}
	return path, nil
}

// Remove deletes a workbook and its manifest. The name is validated against
// the catalogue directory so a crafted request cannot reach outside it.
func (c *Catalogue) Remove(name string) error {
	full, err := c.Resolve(name)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil {
		return err
	}
	os.Remove(manifestPath(full))
	c.Invalidate()
	return nil
}

// Resolve turns a user-supplied filename into a path inside the exports
// folder, rejecting anything that escapes it.
func (c *Catalogue) Resolve(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid file name")
	}
	if !isExport(name) {
		return "", fmt.Errorf("not a spreadsheet")
	}
	full := filepath.Join(c.dir, filepath.Base(name))
	if _, err := os.Stat(full); err != nil {
		return "", fmt.Errorf("no such export")
	}
	return full, nil
}
