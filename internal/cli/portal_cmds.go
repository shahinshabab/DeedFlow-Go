package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/mail"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

func init() {
	commands["seed_test_deeds"] = seedTestDeeds
	commands["run_test_submissions"] = runTestSubmissions
	commands["run_test_edit"] = runTestEdit
	commands["scrape_portal_dropdowns"] = scrapePortalDropdowns
	commands["probe_portal_page"] = probePortalPage
	commands["probe_row_actions"] = probeRowActions
	commands["probe_fair_value"] = probeFairValue
}

var tokenPattern = func(s string) bool {
	if len(s) < 7 || s[0] != 'T' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func userByName(ctx context.Context, e *Env, username string) (*models.User, error) {
	u, err := e.Store.UserByUsername(ctx, username)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	if u == nil || errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("No user named %q.", username)
	}
	return u, nil
}

// portalCredentialFor is the portal login a user submits with: their
// organization's.
func portalCredentialFor(ctx context.Context, e *Env, user *models.User) (*models.PortalCredential, error) {
	m, err := e.Store.MembershipOf(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("%s doesn't belong to an organization, so there is no portal login to use", user.Username)
	}
	cred, err := e.Store.PortalCredentialFor(ctx, m.OrganizationID)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, fmt.Errorf("the organization of %s has no portal login yet", user.Username)
	}
	return cred, nil
}

func seedTestDeeds(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("seed_test_deeds", flag.ContinueOnError)
	owner := fs.String("owner", "", "username the deeds belong to")
	count := fs.Int("count", 5, fmt.Sprintf("how many (max %d)", pipeline.SeedSpecCount()))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *owner == "" {
		return errors.New("--owner is required")
	}
	user, err := userByName(ctx, e, *owner)
	if err != nil {
		return err
	}
	if *count < 1 || *count > pipeline.SeedSpecCount() {
		return fmt.Errorf("--count must be between 1 and %d", pipeline.SeedSpecCount())
	}
	today := deeds.Today(e.Cfg.TimeZone)
	for i := 0; i < *count; i++ {
		deed, err := pipeline.SeedTestDeed(ctx, e.Store, user.ID, i, today)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s -> /deeds/%s/\n", deed.Title, deed.PublicID)
	}
	return nil
}

// runTestSubmissions submits the test deeds to the portal one after another,
// in the foreground, and prints how each run went. To avoid leaving one portal
// token per test deed behind, every deed after the first reuses a single
// token: the first deed creates it (or --token names an existing one), and
// each later deed updates that same token - every page except registration is
// cleared on the portal and re-entered with that deed's data.
func runTestSubmissions(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("run_test_submissions", flag.ContinueOnError)
	owner := fs.String("owner", "", "username the test deeds belong to")
	which := fs.String("deeds", "", "comma-separated positions (1 = oldest test deed)")
	token := fs.String("token", "", "existing portal token to reuse for every deed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *owner == "" {
		return errors.New("--owner is required")
	}
	all, err := e.Store.TestDeedsOf(ctx, *owner)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return errors.New("No test deeds - run seed_test_deeds first.")
	}
	selected := all
	if *which != "" {
		selected = nil
		for _, part := range strings.Split(*which, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(all) {
				return fmt.Errorf("--deeds: %q is not a position between 1 and %d", part, len(all))
			}
			selected = append(selected, all[n-1])
		}
	}
	env := e.pipelineEnv()
	shared := *token
	for i := range selected {
		deed := &selected[i]
		if err := e.Store.DeleteScreenshots(ctx, deed.ID); err != nil {
			return err
		}
		deed.TokenNumber = shared
		// With a shared token every page except registration (fixed once the
		// token exists) is sent again; nothing is "already there".
		data, err := e.Store.LoadDeedData(ctx, deed)
		if err != nil {
			return err
		}
		payload, err := deeds.BuildPayload(data)
		if err != nil {
			return err
		}
		snapshot := models.NewSnapshot()
		if shared != "" {
			snapshot.Sections["registration"] = deeds.SectionFingerprints(payload)["registration"]
		}
		deed.PortalSnapshot = snapshot
		deed.Status = models.StatusSubmitting
		deed.Progress = 0
		deed.ResultMessage = ""
		if err := e.Store.SaveDeed(ctx, deed); err != nil {
			return err
		}
		kind := "new token"
		if shared != "" {
			kind = "token " + shared
		}
		fmt.Printf("\n=== %s (%s)\n", deed.Title, kind)
		started := time.Now()
		if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
			fmt.Printf("task raised: %v\n", err)
		}
		fresh, err := e.Store.DeedByID(ctx, deed.ID)
		if err != nil {
			return err
		}
		if shared == "" {
			shared = fresh.TokenNumber
		}
		shots, err := e.Store.RecentScreenshots(ctx, deed.ID, 1000)
		if err != nil {
			return err
		}
		names := []string{}
		for j := len(shots) - 1; j >= 0; j-- {
			names = append(names, shots[j].Image)
		}
		fmt.Println(prettyJSON(map[string]any{
			"status": fresh.Status, "seconds": int(time.Since(started).Seconds() + 0.5), "token": fresh.TokenNumber,
			"message": fresh.ResultMessage, "screenshots": names,
		}))
	}
	return nil
}

func prettyJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// setByTag sets the model field whose db tag is name from text, returning the
// old and new values for display.
func setByTag(row any, name, text string) (old, now any, err error) {
	v := reflect.ValueOf(row).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("db") != name {
			continue
		}
		f := v.Field(i)
		old = reflect.Indirect(f).Interface()
		if f.Kind() == reflect.Ptr && f.IsNil() {
			old = nil
		}
		switch x := f.Addr().Interface().(type) {
		case *string:
			*x = text
		case **int16:
			n, err := strconv.ParseInt(text, 10, 16)
			if err != nil {
				return nil, nil, err
			}
			n16 := int16(n)
			*x = &n16
		case **int32:
			n, err := strconv.ParseInt(text, 10, 32)
			if err != nil {
				return nil, nil, err
			}
			n32 := int32(n)
			*x = &n32
		case *int16, *int32, *int64:
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, nil, err
			}
			f.SetInt(n)
		case **db.Decimal:
			d, err := db.ParseDecimal(text)
			if err != nil {
				return nil, nil, err
			}
			*x = &d
		default:
			return nil, nil, fmt.Errorf("can't set %s from the command line", name)
		}
		return old, reflect.Indirect(f).Interface(), nil
	}
	return nil, nil, fmt.Errorf("a row has no field called %q", name)
}

// runTestEdit tries the edit pipeline on a real token: it changes one field of
// one row of the deed that holds --token, then sends the edit exactly as the
// "Send edits to portal" button does (same job, run in this process), and
// prints how it went. For dummy tokens only - it really updates that row on the
// portal.
func runTestEdit(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("run_test_edit", flag.ContinueOnError)
	token := fs.String("token", "", "the portal token")
	list := fs.String("list", "", "executants, claimants, schedule or witnesses")
	index := fs.Int("index", 0, "the row's 1-based position in the deed's own order")
	var changes listFlag
	fs.Var(&changes, "set", "FIELD=VALUE (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tables := map[string]string{"executants": "executants", "claimants": "claimants", "schedule": "schedule", "witnesses": "witnesses"}
	table, ok := tables[*list]
	if !ok || *token == "" || *index < 1 {
		return errors.New("--token, --list (executants, claimants, schedule, witnesses) and --index (>= 1) are required")
	}
	deed, err := e.Store.DeedHoldingToken(ctx, *token)
	if err != nil {
		return err
	}
	if deed == nil {
		return fmt.Errorf("No deed holds token %s.", *token)
	}
	fmt.Printf("Deed %d (%s) status=%s\n", deed.ID, deed, deed.Status)
	env := e.pipelineEnv()
	if err := env.EnsureBaseline(ctx, deed); err != nil {
		return err
	}
	if deed, err = e.Store.DeedByID(ctx, deed.ID); err != nil {
		return err
	}
	counts := map[string]int{}
	for k, v := range deed.PortalSnapshot.Rows {
		counts[k] = len(v)
	}
	fmt.Printf("Rows on record: %v\n", counts)

	err = e.Store.InTx(ctx, func(tx *store.Store) error {
		data, err := tx.LoadDeedData(ctx, deed)
		if err != nil {
			return err
		}
		var row any
		var pos int
		switch *list {
		case "executants":
			pos = len(data.Executants)
			if *index <= pos {
				row = &data.Executants[*index-1]
			}
		case "claimants":
			pos = len(data.Claimants)
			if *index <= pos {
				row = &data.Claimants[*index-1]
			}
		case "schedule":
			pos = len(data.Schedule)
			if *index <= pos {
				row = &data.Schedule[*index-1]
			}
		case "witnesses":
			pos = len(data.Witnesses)
			if *index <= pos {
				row = &data.Witnesses[*index-1]
			}
		}
		if row == nil {
			return fmt.Errorf("the deed has only %d %s", pos, *list)
		}
		for _, pair := range changes {
			field, value, _ := strings.Cut(pair, "=")
			old, now, err := setByTag(row, field, value)
			if err != nil {
				return err
			}
			fmt.Printf("%s %d: %s %v -> %v\n", *list, *index, field, old, now)
		}
		if err := tx.UpdateRow(ctx, store.RowTable[table], row); err != nil {
			return err
		}
		fresh, err := tx.LoadDeedData(ctx, deed)
		if err != nil {
			return err
		}
		rows, whole := deeds.PendingChanges(fresh)
		fmt.Printf("Will send - rows: %v; whole pages: %v\n", rows, whole)
		if len(whole) > 0 || len(rows) == 0 {
			// Only the row edit is being tried here - never a whole-tab
			// re-entry. Undo the change.
			return errors.New("This change wouldn't go through the row edit - nothing sent, change undone.")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := e.Store.StartSubmission(ctx, deed.ID); err != nil {
		return err
	}
	if err := env.SubmitDeedToPortal(ctx, deed.ID); err != nil {
		return err
	}
	if deed, err = e.Store.DeedByID(ctx, deed.ID); err != nil {
		return err
	}
	fmt.Printf("Result: %s - %s\n", deed.Status, deed.ResultMessage)
	shots, err := e.Store.RecentScreenshots(ctx, deed.ID, 12)
	if err != nil {
		return err
	}
	for _, s := range shots {
		fmt.Printf("  screenshot %s: %s\n", s.Label, s.Image)
	}
	return nil
}

// withBrowserLock takes the same lock as deed submissions, so the two never
// fight over the Selenium grid's one Chrome; a submission started meanwhile
// waits.
func withBrowserLock(ctx context.Context, e *Env, wait time.Duration, fn func() error) error {
	deadline := time.Now().Add(wait)
	for {
		lock, ok, err := e.DB.TryLock(ctx, pipeline.BrowserLockKey())
		if err != nil {
			return err
		}
		if ok {
			defer lock.Release()
			return fn()
		}
		if time.Now().After(deadline) {
			return errors.New("A deed submission is using the browser - try again later.")
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// openOwnerPortal starts a browser session with the owner's organization's login.
func openOwnerPortal(ctx context.Context, e *Env, owner string, deadline time.Time) (*portal.Automation, error) {
	if owner == "" {
		return nil, errors.New("--owner is required")
	}
	user, err := userByName(ctx, e, owner)
	if err != nil {
		return nil, err
	}
	cred, err := portalCredentialFor(ctx, e, user)
	if err != nil {
		return nil, err
	}
	return pipeline.OpenPortal(ctx, e.Cfg, cred, deadline)
}

// scrapePortalDropdowns reads every dropdown on the registration portal -
// every level of every cascading one - and stores them for the wizard (see
// internal/portal/scraper.go for how, and why it never saves anything on the
// portal). Pages past the New Token page live inside a token, so --token names
// an existing one to read them on (it is only opened, never changed). The full
// result is always written to --out as JSON too; --load stores such a file
// without scraping.
func scrapePortalDropdowns(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("scrape_portal_dropdowns", flag.ContinueOnError)
	owner := fs.String("owner", "", "user whose portal login to use")
	token := fs.String("token", "", "existing token to read the token pages on (opened, never changed)")
	pages := fs.String("pages", "", "comma-separated page keys (default: all)")
	maxOptions := fs.Int("max-options", 0, "walk only the first N options at each level (trial runs)")
	out := fs.String("out", filepath.Join(e.Cfg.MediaRoot, "portal_dropdowns.json"), "where to write the JSON")
	noSave := fs.Bool("no-save", false, "only write the JSON file")
	load := fs.String("load", "", "store an earlier scrape's JSON file and exit")
	wait := fs.Int("wait-for-browser", 1800, "seconds to wait for a running submission to free the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var pageKeys []string
	if *pages != "" {
		pageKeys = strings.Split(*pages, ",")
	}
	if *load != "" {
		counts, err := importScrapeFile(ctx, e, *load, pageKeys)
		if err != nil {
			return err
		}
		fmt.Printf("Stored: %v\n", counts)
		return nil
	}
	if *token != "" && !tokenPattern(*token) {
		return fmt.Errorf("'%s' doesn't look like a token number.", *token)
	}
	var result *portal.ScrapeResult
	started := time.Now()
	err := withBrowserLock(ctx, e, time.Duration(*wait)*time.Second, func() error {
		bot, err := openOwnerPortal(ctx, e, *owner, time.Time{})
		if err != nil {
			return err
		}
		defer bot.Close()
		scraper := portal.NewScraper(bot, *token, *maxOptions, func(msg string) { fmt.Println(msg) })
		result = scraper.Scrape(pageKeys)
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(result); err != nil {
		return err
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s in %ds\n", *out, int(time.Since(started).Seconds()+0.5))
	for _, key := range result.PageOrder {
		page := result.Pages[key]
		fmt.Printf("  %s: %d dropdowns, %d option lists\n", key, len(page.Fields), len(page.Rows))
	}
	for key, why := range result.Failed {
		fmt.Printf("  %s FAILED: %s\n", key, why)
	}
	if !*noSave {
		counts, err := importScrapeFile(ctx, e, *out, nil)
		if err != nil {
			return err
		}
		fmt.Printf("Stored: %v\n", counts)
	}
	return nil
}

func importScrapeFile(ctx context.Context, e *Env, path string, pages []string) (map[string]int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file deeds.ScrapeFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return e.Store.ImportScrape(ctx, &file, pages)
}

func probeFlags(name string, args []string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	owner := fs.String("owner", "", "user whose portal login to use")
	token := fs.String("token", "", "the portal token to look at")
	return fs, owner, token, nil
}

func probePortalPage(ctx context.Context, e *Env, args []string) error {
	fs, owner, token, _ := probeFlags("probe_portal_page", args)
	tab := fs.String("tab", "li_prevDoc", "the wizard tab (li_prevDoc, li_executant, ...)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	bot, err := openOwnerPortal(ctx, e, *owner, time.Time{})
	if err != nil {
		return err
	}
	defer bot.Close()
	return bot.ProbePortalPage(*token, *tab, e.Cfg.MediaRoot, func(s string) { fmt.Println(s) })
}

func probeFairValue(ctx context.Context, e *Env, args []string) error {
	fs, owner, token, _ := probeFlags("probe_fair_value", args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	bot, err := openOwnerPortal(ctx, e, *owner, time.Time{})
	if err != nil {
		return err
	}
	defer bot.Close()
	return bot.ProbeFairValue(*token, func(s string) { fmt.Println(s) })
}

func probeRowActions(ctx context.Context, e *Env, args []string) error {
	fs, owner, token, _ := probeFlags("probe_row_actions", args)
	tabs := fs.String("tabs", "li_prevDoc,li_executant,li_claimant,li_property,li_witness,li_endorse_witness", "comma-separated tabs")
	shots := fs.String("shots", e.Cfg.MediaRoot, "where to save screenshots")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	bot, err := openOwnerPortal(ctx, e, *owner, time.Time{})
	if err != nil {
		return err
	}
	defer bot.Close()
	var ids []string
	for _, t := range strings.Split(*tabs, ",") {
		if t != "" {
			ids = append(ids, t)
		}
	}
	return bot.ProbeRowActions(*token, ids, *shots, func(s string) { fmt.Println(s) })
}

func init() { commands["send_test_email"] = sendTestEmail }

// sendTestEmail sends one test email, to check the server's mail settings.
func sendTestEmail(ctx context.Context, e *Env, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: deedflow send_test_email you@example.com")
	}
	sender := mail.New(e.Cfg)
	msg := &mail.Message{From: e.Cfg.DefaultFromEmail, To: []string{args[0]}, Subject: "DeedFlow test email",
		Text: "If you can read this, DeedFlow can send email."}
	if err := sender.Send(ctx, msg); err != nil {
		return err
	}
	fmt.Printf("Sent from %s to %s via %s.\n", e.Cfg.DefaultFromEmail, args[0], sender.Description())
	return nil
}
