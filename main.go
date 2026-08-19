// Command goldstar collects supplier invoices from a Hostinger mailbox,
// extracts vehicle registrations, part numbers and VAT figures with Gemini,
// and keeps everything in a local SQLite database with daily Excel exports
// and a local dashboard.
package main

import (
	// Embedded IANA timezone data, so "Europe/London" resolves on a static
	// binary and on machines without a system zoneinfo database.
	_ "time/tzdata"

	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"goldstar/internal/auth"
	"goldstar/internal/config"
	"goldstar/internal/extract"
	"goldstar/internal/pipeline"
	"goldstar/internal/store"
	"goldstar/internal/web"
)

const usage = `goldstar — invoice collector for Garage Goldstar

Usage:
  goldstar           Open the dashboard (same as ` + "`serve`" + `). Makes no API calls
                     until you press Sync or upload a file.
  goldstar run       Fetch new invoices, then write today's Excel export (the daily job)
  goldstar fetch     Fetch and extract new invoices only
  goldstar ingest F  Extract invoices from local PDF or image files
  goldstar export    Write an Excel export from what is already stored
  goldstar backup    Snapshot the database into data/backups and prune old ones
  goldstar examples  Scan the examples folder for new reference invoices
  goldstar eval      Re-extract every completed example and score the accuracy
  goldstar serve     Serve the dashboard (default 127.0.0.1:8787)
  goldstar passwd    Generate the dashboard password hash
  goldstar doctor    Check configuration and connectivity without changing anything
  goldstar models    List the Gemini models this API key can call

Configuration is read from .env beside the binary, then $HOME/.config/garage-goldstar/.env.
See .env.example for the available settings.`

func main() {
	log.SetFlags(log.Ltime)

	// Bare `goldstar` opens the dashboard. It used to mean `run`, which
	// contacts the mailbox and spends Gemini credit — a bad thing to do by
	// accident. `serve` makes no API calls until a button is pressed, so it
	// is the safe thing to land on.
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Println(usage)
		return
	}

	if err := realMain(cmd); err != nil {
		log.Fatalf("error: %v", err)
	}
}

// logf sends pipeline progress to the terminal.
var logf = pipeline.LogFunc(func(format string, args ...any) { log.Printf(format, args...) })

func realMain(cmd string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// passwd needs no database and must work before anything is configured.
	if cmd == "passwd" {
		return passwd()
	}

	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	// Ctrl-C and systemd stop both unwind cleanly mid-fetch.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "run":
		if err := runFetch(ctx, cfg, db); err != nil {
			return err
		}
		return runExport(cfg, db)
	case "fetch":
		return runFetch(ctx, cfg, db)
	case "ingest":
		files := os.Args[2:]
		if len(files) == 0 {
			return fmt.Errorf("usage: goldstar ingest <file.pdf> [more files…]")
		}
		st, err := pipeline.IngestFile(ctx, cfg, db, files, logf)
		if st != nil {
			log.Print(st.Summary())
		}
		return err
	case "export":
		return runExport(cfg, db)
	case "backup":
		path, err := pipeline.RunBackup(cfg, db)
		if err != nil {
			return err
		}
		if path == "" {
			return fmt.Errorf("backups are disabled (GOLDSTAR_BACKUP_KEEP=0)")
		}
		log.Printf("wrote %s", path)
		return nil
	case "examples":
		added, seen, err := pipeline.ScanExamples(cfg, db, logf)
		if err != nil {
			return err
		}
		log.Printf("examples folder %s: %d file(s), %d newly registered", cfg.ExamplesDir(), seen, added)
		if added > 0 {
			log.Printf("enter their correct values on the Training page to activate them")
		}
		return nil
	case "eval":
		results, err := pipeline.Eval(ctx, cfg, db, logf)
		if err != nil {
			return err
		}
		ok, all := 0, 0
		for _, r := range results {
			ok += r.FieldsOK
			all += r.FieldsAll
		}
		if all == 0 {
			return fmt.Errorf("no comparable fields in the stored ground truth")
		}
		log.Printf("accuracy %.1f%% (%d/%d fields)", float64(ok)/float64(all)*100, ok, all)
		return nil
	case "serve":
		srv, err := web.New(cfg, db)
		if err != nil {
			return err
		}
		return srv.Listen(cfg.WebAddr)
	case "models":
		if err := cfg.RequireGemini(); err != nil {
			return err
		}
		names, err := extract.ListModels(ctx, cfg.GeminiKey)
		if err != nil {
			return err
		}
		for _, n := range names {
			marker := "  "
			if n == cfg.GeminiModel {
				marker = "* "
			}
			fmt.Println(marker + n)
		}
		return nil
	case "doctor":
		return doctor(cfg, db)
	default:
		fmt.Println(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func runFetch(ctx context.Context, cfg *config.Config, db *store.Store) error {
	st, err := pipeline.Fetch(ctx, cfg, db, logf)
	if st != nil {
		log.Print(st.Summary())
	}
	return err
}

func runExport(cfg *config.Config, db *store.Store) error {
	path, n, err := pipeline.Export(cfg, db)
	if err != nil {
		return err
	}
	log.Printf("wrote %d invoice(s) to %s", n, path)
	return nil
}

// passwd prompts twice without echoing and prints the line to add to .env.
// When stdin is not a terminal it reads a single line instead, so the hash can
// be generated from a script.
func passwd() error {
	var password string

	if term.IsTerminal(int(syscall.Stdin)) {
		fmt.Fprint(os.Stderr, "New dashboard password (min 10 chars): ")
		first, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		fmt.Fprint(os.Stderr, "Repeat: ")
		second, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if string(first) != string(second) {
			return fmt.Errorf("passwords do not match")
		}
		password = string(first)
	} else {
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return fmt.Errorf("no password on stdin")
		}
		password = strings.TrimSpace(sc.Text())
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nAdd this line to the .env beside the binary:\n\n")
	fmt.Printf("GOLDSTAR_PASSWORD_HASH=%s\n", hash)
	return nil
}

// doctor reports what is configured and what is missing, without sending any
// invoice to Gemini or writing any row.
func doctor(cfg *config.Config, db *store.Store) error {
	fmt.Printf("data dir     %s\n", cfg.DataDir)
	fmt.Printf("database     %s\n", cfg.DBPath())
	fmt.Printf("exports      %s\n", cfg.ExportsDir())
	fmt.Printf("imap         %s:%d mailbox=%s\n", cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPMailbox)
	fmt.Printf("imap user    %s\n", orUnset(cfg.IMAPUser))
	fmt.Printf("imap pass    %s\n", secretState(cfg.IMAPPass))
	fmt.Printf("gemini key   %s\n", secretState(cfg.GeminiKey))
	fmt.Printf("gemini model %s\n", cfg.GeminiModel)
	fmt.Printf("web addr     %s\n", cfg.WebAddr)
	fmt.Printf("web password %s\n", secretState(cfg.PasswordHash))
	fmt.Printf("cookie secure %v\n", cfg.CookieSecure)

	invoices, items, err := db.Counts()
	if err != nil {
		return err
	}
	fmt.Printf("stored       %d invoice(s), %d line item(s)\n", invoices, items)

	// The combination that would publish financial records to the internet.
	switch {
	case cfg.PasswordHash != "":
		fmt.Printf("\nDASHBOARD   password set\n")
		if !cfg.CookieSecure {
			fmt.Printf("            note: set GOLDSTAR_COOKIE_SECURE=true when serving through a tunnel\n")
		}
	case cfg.AllowNoPassword:
		fmt.Printf("\nDASHBOARD   NO PASSWORD (explicitly allowed). Anyone who can reach %s sees every invoice.\n", cfg.WebAddr)
	default:
		fmt.Printf("\nDASHBOARD   REFUSES TO START: no password set.\n")
		fmt.Printf("            Run `goldstar passwd` and add the printed line to your .env.\n")
	}

	if err := cfg.RequireMail(); err != nil {
		fmt.Printf("IMAP        NOT CONFIGURED: %v\n", err)
		return nil
	}
	fmt.Printf("connecting to IMAP…\n")
	n, err := pipeline.CheckMail(cfg)
	if err != nil {
		fmt.Printf("IMAP        FAILED: %v\n", err)
		return nil
	}
	fmt.Printf("IMAP        OK — %d message(s) in the last %d day(s)\n", n, cfg.LookbackDays)
	return nil
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func secretState(s string) string {
	if s == "" {
		return "(unset)"
	}
	return fmt.Sprintf("set, %d chars", len(s))
}
