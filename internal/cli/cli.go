// Package cli implements the deedflow command line: the web server, the
// background worker and the management commands (what manage.py offered).
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/fernet"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/internal/web"
)

const usage = `deedflow - automates preparing and filing deed registrations

usage: deedflow <command> [options]

server:
  serve                   run the web server (default)
  worker                  run the background worker (PDF processing, portal submission, nightly receipts)
  all                     run the web server and a worker in one process
  migrate                 create / update the database tables

accounts:
  createsuperuser         create a site administrator (--username, --email; password is prompted)
  setup_organization      create an organization with its admin, or rename one
  genkey                  print a new FIELD_ENCRYPTION_KEY

testing and maintenance:
  seed_test_deeds         create five complete test deeds for an organization
  run_test_submissions    submit test deeds to a stand-in or the real portal
  run_test_edit           edit a submitted token's rows on the portal
  scrape_portal_dropdowns read the portal's dropdown options into the database
  probe_portal_page       print what the portal shows on a page (read-only)
  probe_row_actions       print a token tab's row actions (read-only)
  probe_fair_value        probe the fair value lookup (read-only)
  send_test_email         send one test email to check the mail settings
`

// Run executes a command.
func Run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "genkey":
		k, err := fernet.GenerateKey()
		if err != nil {
			return err
		}
		fmt.Println(k)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(cfg)}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}
	app := &Env{Cfg: cfg, DB: database, Store: store.New(database), Log: log}
	app.Hub = events.NewHub(database, log)
	app.Queue = jobs.New(database, log)

	switch cmd {
	case "migrate":
		fmt.Println("database is up to date")
		return nil
	case "serve":
		return app.serve(ctx, false)
	case "all":
		return app.serve(ctx, true)
	case "worker":
		return app.worker(ctx)
	}
	if run, ok := commands[cmd]; ok {
		return run(ctx, app, args)
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return errors.New("unknown command")
}

func logLevel(cfg *config.Config) slog.Level {
	if cfg.Debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// Env is what every command works with.
type Env struct {
	Cfg   *config.Config
	DB    *db.DB
	Store *store.Store
	Hub   *events.Hub
	Queue *jobs.Queue
	Log   *slog.Logger
}

// commands are the management commands registered by other files.
var commands = map[string]func(ctx context.Context, e *Env, args []string) error{}

func (e *Env) serve(ctx context.Context, withWorker bool) error {
	app, err := web.New(e.Cfg, e.Store, e.Hub, e.Queue, e.Log)
	if err != nil {
		return err
	}
	go e.Hub.Run(ctx)
	if withWorker {
		go func() {
			if err := e.worker(ctx); err != nil {
				e.Log.Error("worker stopped", "err", err)
			}
		}()
	}
	srv := &http.Server{
		Addr:              e.Cfg.Addr,
		Handler:           app,
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		e.Log.Info("DeedFlow listening", "addr", e.Cfg.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// prompt reads a line; secret reads without echo when stdin is a terminal.
func prompt(label string, secret bool) (string, error) {
	fmt.Fprint(os.Stderr, label)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

type storeT = store.Store
