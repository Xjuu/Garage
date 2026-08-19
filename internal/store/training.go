package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Example is one reference invoice plus the values a human confirmed are
// correct. Until truth is filled in its status is "pending" and it is used for
// nothing — a half-entered example would teach the model the wrong thing.
type Example struct {
	ID         int64  `json:"id"`
	FileSHA256 string `json:"file_sha256"`
	Filename   string `json:"filename"`
	SourceFile string `json:"source_file"`
	MIMEType   string `json:"mime_type"`
	Supplier   string `json:"supplier"`
	TruthJSON  string `json:"truth_json"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

const (
	ExamplePending = "pending"
	ExampleReady   = "ready"
)

// AddExample registers a file found in the examples folder. Re-scanning is
// safe: the content hash keeps one row per document.
func (s *Store) AddExample(sha, filename, sourceFile, mimeType string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM examples WHERE file_sha256 = ?`, sha).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	res, err := s.db.Exec(`
		INSERT INTO examples (file_sha256, filename, source_file, mime_type, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		sha, filename, sourceFile, mimeType, ExamplePending, now(), now())
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}

func (s *Store) Examples() ([]Example, error) {
	rows, err := s.db.Query(`
		SELECT id, file_sha256, filename, source_file, mime_type, supplier,
		       truth_json, status, created_at, updated_at
		FROM examples ORDER BY status, supplier, filename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Example{}
	for rows.Next() {
		var e Example
		if err := rows.Scan(&e.ID, &e.FileSHA256, &e.Filename, &e.SourceFile, &e.MIMEType,
			&e.Supplier, &e.TruthJSON, &e.Status, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetExample(id int64) (*Example, error) {
	var e Example
	err := s.db.QueryRow(`
		SELECT id, file_sha256, filename, source_file, mime_type, supplier,
		       truth_json, status, created_at, updated_at
		FROM examples WHERE id = ?`, id).
		Scan(&e.ID, &e.FileSHA256, &e.Filename, &e.SourceFile, &e.MIMEType,
			&e.Supplier, &e.TruthJSON, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// SaveExampleTruth stores the corrected values. Supplying valid JSON marks the
// example ready; clearing it sends it back to pending.
func (s *Store) SaveExampleTruth(id int64, supplier, truthJSON string) error {
	truthJSON = strings.TrimSpace(truthJSON)
	status := ExamplePending
	if truthJSON != "" {
		var probe map[string]any
		if err := json.Unmarshal([]byte(truthJSON), &probe); err != nil {
			return fmt.Errorf("ground truth is not valid JSON: %w", err)
		}
		status = ExampleReady
	}
	_, err := s.db.Exec(
		`UPDATE examples SET supplier = ?, truth_json = ?, status = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(supplier), truthJSON, status, now(), id)
	return err
}

func (s *Store) DeleteExample(id int64) error {
	_, err := s.db.Exec(`DELETE FROM examples WHERE id = ?`, id)
	return err
}

// ReadyExamples are the completed ones, the only kind used for prompting or
// evaluation.
func (s *Store) ReadyExamples() ([]Example, error) {
	all, err := s.Examples()
	if err != nil {
		return nil, err
	}
	out := []Example{}
	for _, e := range all {
		if e.Status == ExampleReady && e.TruthJSON != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// ── Supplier hints ────────────────────────────────────────────────────────

type Hint struct {
	ID        int64  `json:"id"`
	Supplier  string `json:"supplier"`
	Hint      string `json:"hint"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) Hints() ([]Hint, error) {
	rows, err := s.db.Query(
		`SELECT id, supplier, hint, updated_at FROM supplier_hints ORDER BY LOWER(supplier)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hint{}
	for rows.Next() {
		var h Hint
		if err := rows.Scan(&h.ID, &h.Supplier, &h.Hint, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) SaveHint(supplier, hint string) error {
	supplier = strings.TrimSpace(supplier)
	if supplier == "" {
		return fmt.Errorf("supplier is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO supplier_hints (supplier, hint, updated_at) VALUES (?,?,?)
		ON CONFLICT(supplier) DO UPDATE SET hint = excluded.hint, updated_at = excluded.updated_at`,
		supplier, strings.TrimSpace(hint), now())
	return err
}

func (s *Store) DeleteHint(id int64) error {
	_, err := s.db.Exec(`DELETE FROM supplier_hints WHERE id = ?`, id)
	return err
}

// AllHints returns every hint as supplier -> text. The extractor cannot know
// the supplier before reading the document, so it receives the whole set and
// the model applies whichever matches.
func (s *Store) AllHints() (map[string]string, error) {
	hints, err := s.Hints()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(hints))
	for _, h := range hints {
		if h.Hint != "" {
			out[h.Supplier] = h.Hint
		}
	}
	return out, nil
}

// ── Eval runs ─────────────────────────────────────────────────────────────

type EvalRun struct {
	ID         int64   `json:"id"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at"`
	Model      string  `json:"model"`
	Examples   int     `json:"examples"`
	FieldsOK   int     `json:"fields_ok"`
	FieldsAll  int     `json:"fields_all"`
	Accuracy   float64 `json:"accuracy"`
	DetailJSON string  `json:"detail_json"`
}

func (s *Store) StartEvalRun(model string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO eval_runs (started_at, model) VALUES (?,?)`, now(), model)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishEvalRun(id int64, examples, ok, all int, detail string) error {
	_, err := s.db.Exec(`
		UPDATE eval_runs SET finished_at = ?, examples = ?, fields_ok = ?, fields_all = ?, detail_json = ?
		WHERE id = ?`, now(), examples, ok, all, detail, id)
	return err
}

func (s *Store) EvalRuns(limit int) ([]EvalRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, started_at, finished_at, model, examples, fields_ok, fields_all, detail_json
		FROM eval_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EvalRun{}
	for rows.Next() {
		var r EvalRun
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Model,
			&r.Examples, &r.FieldsOK, &r.FieldsAll, &r.DetailJSON); err != nil {
			return nil, err
		}
		if r.FieldsAll > 0 {
			r.Accuracy = float64(r.FieldsOK) / float64(r.FieldsAll) * 100
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
