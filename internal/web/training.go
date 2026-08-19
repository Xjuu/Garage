package web

import (
	"context"

	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"goldstar/internal/extract"
	"goldstar/internal/pipeline"
)

// routesTraining registers the example library, supplier hints, and the eval
// regression run — the machinery that teaches the extractor.
func (s *Server) routesTraining(api *http.ServeMux) {
	api.HandleFunc("GET /api/examples", s.json(s.examples))
	api.HandleFunc("POST /api/examples/scan", s.json(s.scanExamples))
	api.HandleFunc("POST /api/examples/upload", s.json(s.uploadExample))
	api.HandleFunc("PATCH /api/examples/{id}", s.json(s.saveExample))
	api.HandleFunc("DELETE /api/examples/{id}", s.json(s.deleteExample))
	api.HandleFunc("POST /api/examples/{id}/prefill", s.json(s.prefillExample))
	api.HandleFunc("GET /api/examples/{id}/doc", s.exampleDoc)

	api.HandleFunc("GET /api/hints", s.json(s.hints))
	api.HandleFunc("POST /api/hints", s.json(s.saveHint))
	api.HandleFunc("DELETE /api/hints/{id}", s.json(s.deleteHint))

	api.HandleFunc("POST /api/eval", s.json(s.startEval))
	api.HandleFunc("GET /api/evals", s.json(s.evalRuns))
}

func (s *Server) examples(r *http.Request) (any, error) {
	list, err := s.db.Examples()
	if err != nil {
		return nil, err
	}
	return map[string]any{"examples": list, "folder": s.cfg.ExamplesDir()}, nil
}

func (s *Server) scanExamples(r *http.Request) (any, error) {
	added, seen, err := pipeline.ScanExamples(s.cfg, s.db, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"added": added, "seen": seen, "folder": s.cfg.ExamplesDir()}, nil
}

// uploadExample saves a file straight into the examples folder, so dropping it
// on the Training page and copying it into the folder by hand are equivalent.
func (s *Server) uploadExample(r *http.Request) (any, error) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		return nil, fail(http.StatusBadRequest, "bad upload: %v", err)
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		return nil, fail(http.StatusBadRequest, "no files")
	}
	if err := os.MkdirAll(s.cfg.ExamplesDir(), 0o700); err != nil {
		return nil, err
	}

	saved := 0
	for _, fh := range files {
		if fh.Size > maxUploadBytes {
			return nil, fail(http.StatusRequestEntityTooLarge, "%s is too large", fh.Filename)
		}
		src, err := fh.Open()
		if err != nil {
			return nil, err
		}
		// filepath.Base defeats a "../.." filename from a crafted client.
		dst := filepath.Join(s.cfg.ExamplesDir(), filepath.Base(fh.Filename))
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			src.Close()
			return nil, err
		}
		_, err = io.Copy(out, io.LimitReader(src, maxUploadBytes))
		src.Close()
		out.Close()
		if err != nil {
			return nil, err
		}
		saved++
	}

	added, seen, err := pipeline.ScanExamples(s.cfg, s.db, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"saved": saved, "added": added, "seen": seen}, nil
}

func (s *Server) saveExample(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Supplier  string          `json:"supplier"`
		TruthJSON json.RawMessage `json:"truth"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}

	truth := strings.TrimSpace(string(body.TruthJSON))
	if truth == "null" {
		truth = ""
	}
	if err := s.db.SaveExampleTruth(id, body.Supplier, truth); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return s.db.GetExample(id)
}

func (s *Server) deleteExample(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	// The file in the examples folder is left alone; deleting the row only
	// removes it from training, and a rescan would pick it up again.
	if err := s.db.DeleteExample(id); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

// prefillExample runs one extraction so the operator corrects a filled-in form
// instead of typing every field from scratch. It deliberately sends no worked
// examples, so the answer reflects what the model does unaided.
func (s *Server) prefillExample(r *http.Request) (any, error) {
	if err := s.cfg.RequireGemini(); err != nil {
		return nil, fail(http.StatusPreconditionFailed, "%v", err)
	}
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	e, err := s.db.GetExample(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(e.SourceFile)
	if err != nil {
		return nil, fail(http.StatusNotFound, "example file missing: %v", err)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*60_000_000_000) // 3 minutes
	defer cancel()

	client, err := extract.New(ctx, s.cfg.GeminiKey, s.cfg.GeminiModel)
	if err != nil {
		return nil, err
	}
	guidance := &extract.Guidance{}
	if hints, err := s.db.AllHints(); err == nil {
		guidance.Hints = hints
	}

	res, _, err := client.Extract(ctx, extract.Document{
		Filename: e.Filename, MIMEType: e.MIMEType, Data: data,
	}, guidance)
	if err != nil {
		return nil, fail(http.StatusBadGateway, "%v", err)
	}
	return res, nil
}

func (s *Server) exampleDoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	e, err := s.db.GetExample(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	abs, err := filepath.Abs(e.SourceFile)
	root, rerr := filepath.Abs(s.cfg.ExamplesDir())
	if err != nil || rerr != nil || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(abs); err != nil {
		http.Error(w, "file missing", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(filepath.Base(abs)))
	http.ServeFile(w, r, abs)
}

// ── hints ─────────────────────────────────────────────────────────────────

func (s *Server) hints(r *http.Request) (any, error) { return s.db.Hints() }

func (s *Server) saveHint(r *http.Request) (any, error) {
	var body struct {
		Supplier string `json:"supplier"`
		Hint     string `json:"hint"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if err := s.db.SaveHint(body.Supplier, body.Hint); err != nil {
		return nil, fail(http.StatusBadRequest, "%v", err)
	}
	return okResponse(), nil
}

func (s *Server) deleteHint(r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteHint(id); err != nil {
		return nil, err
	}
	return okResponse(), nil
}

// ── eval ──────────────────────────────────────────────────────────────────

func (s *Server) startEval(r *http.Request) (any, error) {
	err := s.jobs.Start("eval", func(ctx context.Context, logLine func(string)) (string, error) {
		logf := pipeline.LogFunc(func(format string, args ...any) {
			logLine(fmt.Sprintf(format, args...))
		})
		results, err := pipeline.Eval(ctx, s.cfg, s.db, logf)
		if err != nil {
			return "", err
		}
		ok, all := 0, 0
		for _, r := range results {
			ok += r.FieldsOK
			all += r.FieldsAll
		}
		if all == 0 {
			return "no comparable fields", nil
		}
		return fmt.Sprintf("%.1f%% accuracy (%d/%d fields over %d example(s))",
			float64(ok)/float64(all)*100, ok, all, len(results)), nil
	})
	if err != nil {
		return nil, fail(http.StatusConflict, "%v", err)
	}
	return map[string]bool{"started": true}, nil
}

func (s *Server) evalRuns(r *http.Request) (any, error) { return s.db.EvalRuns(20) }
