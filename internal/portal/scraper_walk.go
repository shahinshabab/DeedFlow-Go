package portal

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// FieldInfo describes one dropdown of a scraped page.
type FieldInfo struct {
	Label   string   `json:"label"`
	Parents []string `json:"parents"`
	Mode    string   `json:"mode"` // static, lookup or page
}

// RowResult is the option list of one dropdown under one choice of its parents.
type RowResult struct {
	Field   string      `json:"field"`
	Parents [][3]string `json:"parents"`
	Options []Option    `json:"options"`
}

// PageResult is one scraped page.
type PageResult struct {
	Label     string                `json:"label"`
	URL       string                `json:"url"`
	Fields    map[string]*FieldInfo `json:"fields"`
	Rows      []RowResult           `json:"rows"`
	Notes     []string              `json:"notes"`
	Navigates []string              `json:"navigates"`
}

// ScrapeResult is the JSON file the scraper writes (and Store.ImportScrape reads).
type ScrapeResult struct {
	Version   int                    `json:"version"`
	ScrapedAt string                 `json:"scraped_at"`
	Token     *string                `json:"token"`
	Pages     map[string]*PageResult `json:"pages"`
	Failed    map[string]string      `json:"failed"`
	// PageOrder is the order pages were read in (not part of the file).
	PageOrder []string `json:"-"`
}

type rowStore struct {
	keys []string
	m    map[string]RowResult
}

func newRowStore() *rowStore { return &rowStore{m: map[string]RowResult{}} }

func rowKey(field string, parents [][3]string) string {
	parts := make([]string, len(parents))
	for i, c := range parents {
		parts[i] = deeds.Normalize(c[2])
	}
	return field + "\x00" + strings.Join(parts, "\x01")
}

// set stores the row, replacing an earlier one in place (dict assignment).
func (r *rowStore) set(row RowResult) {
	k := rowKey(row.Field, row.Parents)
	if _, ok := r.m[k]; !ok {
		r.keys = append(r.keys, k)
	}
	r.m[k] = row
}

func (r *rowStore) setIfNew(row RowResult) {
	if _, ok := r.m[rowKey(row.Field, row.Parents)]; !ok {
		r.set(row)
	}
}

func (r *rowStore) values() []RowResult {
	out := make([]RowResult, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, r.m[k])
	}
	return out
}

func (s *Scraper) scrapePage(page *PageSpec) *PageResult {
	started := time.Now()
	s.bot.recoveries = 0 // the recovery budget is per page here, not per run
	s.openPage(page)
	found := s.discover(page)
	parents, templates := found.parents, found.templates
	children := newDepMap()
	for _, child := range parents.order {
		for _, p := range parents.m[child] {
			children.add(p, child)
		}
	}
	ancestors := func(f string) []string {
		var seen []string
		stack := append([]string(nil), parents.get(f)...)
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !containsStr(seen, p) {
				seen = append(seen, p)
				stack = append(stack, parents.get(p)...)
			}
		}
		return seen
	}

	rows := []RowResult{}
	fields := map[string]*FieldInfo{}
	cascade := map[string]bool{}
	for _, k := range parents.order {
		cascade[k] = true
	}
	for _, k := range children.order {
		cascade[k] = true
	}
	for _, key := range found.order {
		fields[key] = &FieldInfo{Label: found.labels[key], Parents: []string{}, Mode: "static"}
		if !parents.has(key) {
			rows = append(rows, RowResult{Field: key, Parents: [][3]string{}, Options: nonNil(found.rootOptions[key])})
		}
	}

	// Connected groups of cascading dropdowns, each walked on its own.
	var groups [][]string
	seen := map[string]bool{}
	for _, key := range found.order {
		if !cascade[key] || seen[key] {
			continue
		}
		group := map[string]bool{}
		stack := []string{key}
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if group[f] {
				continue
			}
			group[f] = true
			stack = append(stack, parents.get(f)...)
			stack = append(stack, children.get(f)...)
		}
		var members []string
		for _, k := range found.order {
			if group[k] {
				members = append(members, k)
			}
		}
		for k := range group {
			if !containsStr(members, k) {
				members = append(members, k)
			}
		}
		for _, k := range members {
			seen[k] = true
		}
		groups = append(groups, members)
	}

	var notes []string
	for _, group := range groups {
		order := walkOrder(group, parents, children, found.order)
		for _, f := range order {
			var anc []string
			ancF := ancestors(f)
			for _, a := range order {
				if containsStr(ancF, a) {
					anc = append(anc, a)
				}
			}
			if fi, ok := fields[f]; ok {
				fi.Parents = nonNilStrings(anc)
			}
		}
		useRequests := true
		for _, f := range order {
			if len(parents.get(f)) > 0 && templates[f] == nil {
				useRequests = false
			}
		}
		for _, f := range order {
			if len(parents.get(f)) > 0 {
				if fi, ok := fields[f]; ok {
					if useRequests {
						fi.Mode = "lookup"
					} else {
						fi.Mode = "page"
					}
				}
			}
		}
		names := strings.Join(order, " -> ")
		how := "picking on the page"
		if useRequests {
			how = "lookups"
		}
		s.say(fmt.Sprintf("  cascade %s (%s)", names, how))
		groupRows := s.walk(page, order, parents, children, templates, fields, useRequests, found.rootOptions, found.navigates)
		rows = append(rows, groupRows...)
		notes = append(notes, fmt.Sprintf("%s: %d option lists", names, len(groupRows)))
	}

	s.say(fmt.Sprintf("  %s: %d dropdowns, %d option lists in %ds", page.Label, len(fields), len(rows),
		int(time.Since(started).Seconds()+0.5)))
	current := mustv(s.bot.d.CurrentURL(s.bot.ctx))
	nav := []string{}
	for k := range found.navigates {
		nav = append(nav, k)
	}
	sort.Strings(nav)
	return &PageResult{Label: page.Label, URL: current, Fields: fields, Rows: rows, Notes: nonNilStrings(notes), Navigates: nav}
}

func nonNil(o []Option) []Option {
	if o == nil {
		return []Option{}
	}
	return o
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// walkOrder puts parents before children; among the dropdowns ready to go
// next, the one with the deepest cascade below it first, so a lone extra
// parent (e.g. local-body type, alongside the village) is walked innermost
// instead of multiplying the whole geography walk.
func walkOrder(group []string, parents, children *depMap, domOrder []string) []string {
	inGroup := map[string]bool{}
	for _, f := range group {
		inGroup[f] = true
	}
	memo := map[string]int{}
	var depth func(f string) int
	depth = func(f string) int {
		if d, ok := memo[f]; ok {
			return d
		}
		best := 0
		for _, c := range children.get(f) {
			if inGroup[c] {
				if d := depth(c); d > best {
					best = d
				}
			}
		}
		memo[f] = 1 + best
		return memo[f]
	}
	indexOf := func(f string) int {
		for i, k := range domOrder {
			if k == f {
				return i
			}
		}
		return 0
	}
	var order []string
	placed := map[string]bool{}
	for len(order) < len(group) {
		var ready []string
		for _, f := range group {
			if placed[f] {
				continue
			}
			ok := true
			for _, p := range parents.get(f) {
				if !placed[p] {
					ok = false
				}
			}
			if ok {
				ready = append(ready, f)
			}
		}
		if len(ready) == 0 { // a cycle - shouldn't happen; fall back to page order
			for _, f := range group {
				if !placed[f] {
					ready = append(ready, f)
				}
			}
		}
		sort.SliceStable(ready, func(i, j int) bool {
			a, b := ready[i], ready[j]
			if da, db := depth(a), depth(b); da != db {
				return da > db
			}
			if ca, cb := len(children.get(a)), len(children.get(b)); ca != cb {
				return ca > cb
			}
			return indexOf(a) < indexOf(b)
		})
		order = append(order, ready[0])
		placed[ready[0]] = true
	}
	return order
}

func (s *Scraper) limit(options []Option) []Option {
	if s.maxOptions > 0 && len(options) > s.maxOptions {
		return options[:s.maxOptions]
	}
	return options
}

func (s *Scraper) walk(page *PageSpec, order []string, parents, children *depMap, templates map[string]*lookupTemplate,
	fields map[string]*FieldInfo, useRequests bool, rootOptions map[string][]Option, navigates map[string]bool) []RowResult {
	rows := newRowStore()

	if useRequests {
		for _, row := range s.walkBatched(order, parents, children, templates, fields, rootOptions, navigates) {
			rows.set(row)
		}
		return rows.values()
	}

	record := func(f string, assign assignment, options []Option) {
		chain := [][3]string{}
		for _, a := range fields[f].Parents {
			if vt, ok := assign[a]; ok {
				chain = append(chain, [3]string{a, vt[0], vt[1]})
			}
		}
		rows.setIfNew(RowResult{Field: f, Parents: chain, Options: options})
	}
	known := func(assign assignment, among []string) []string {
		var out []string
		for _, x := range among {
			if _, ok := assign[x]; ok {
				out = append(out, x)
			}
		}
		return out
	}
	optionsAt := func(f string, assign assignment) []Option {
		if len(parents.get(f)) == 0 {
			return rootOptions[f]
		}
		for attempt := 1; attempt <= 2; attempt++ {
			var out []Option
			err := try(func() {
				snap := s.snapshot()
				if sel, ok := snap.get(f); ok {
					out = realOptions(sel.Options)
				} else {
					out = []Option{}
				}
			})
			if err == nil {
				return out
			}
			if !isWD(err) {
				panic(err)
			}
			if attempt == 2 {
				panic(err)
			}
			s.pickPath(page, known(assign, order), assign)
		}
		return nil
	}
	var walk func(i int, assign assignment)
	walk = func(i int, assign assignment) {
		if i == len(order) {
			return
		}
		f := order[i]
		opts := optionsAt(f, assign)
		if len(parents.get(f)) > 0 {
			record(f, assign, opts)
		}
		if len(children.get(f)) > 0 && !navigates[f] && len(opts) > 0 {
			limited := s.limit(opts)
			total := len(limited)
			for n, vt := range limited {
				value, text := vt[0], vt[1]
				if i == 0 || (i == 1 && total > 5) {
					s.say(fmt.Sprintf("    %s %d/%d: %s  (%d lists so far)", f, n+1, total, text, len(rows.keys)))
				}
				for attempt := 1; attempt <= 2; attempt++ {
					err := try(func() {
						s.pickPath(page, known(assign, order[:i]), assign)
						s.pick(f, value)
					})
					if err == nil {
						break
					}
					if !isWD(err) || attempt == 2 {
						panic(err)
					}
				}
				next := assignment{}
				for k, v := range assign {
					next[k] = v
				}
				next[f] = [2]string{value, text}
				walk(i+1, next)
			}
		} else {
			walk(i+1, assign)
		}
	}
	walk(0, assignment{})
	return rows.values()
}

// pickPath makes sure every dropdown above this level still shows the choice
// being walked (a lost session or a reload resets them).
func (s *Scraper) pickPath(page *PageSpec, path []string, assign assignment) {
	err := try(func() {
		snap := s.snapshot()
		for _, f := range path {
			sel, ok := snap.get(f)
			if !ok || sel.Value != assign[f][0] {
				panic(errPathChanged)
			}
		}
	})
	if err == nil {
		return
	}
	if err != errPathChanged && !isWD(err) {
		panic(err)
	}
	if err := try(func() {
		if s.loggedOut() {
			s.recoverSession()
		}
	}); err != nil {
		if !isWD(err) {
			panic(err)
		}
		s.recoverSession()
	}
	s.openPage(page)
	for _, f := range path {
		s.pick(f, assign[f][0])
	}
}

var errPathChanged = &Error{Msg: "path changed"}

// walkBatched is the lookup walk, level by level: every list a level needs is
// fetched in one batch, so a level of a few thousand lookups is a few hundred
// round trips rather than a few thousand.
func (s *Scraper) walkBatched(order []string, parents, children *depMap, templates map[string]*lookupTemplate,
	fields map[string]*FieldInfo, rootOptions map[string][]Option, navigates map[string]bool) []RowResult {
	var rows []RowResult
	// Each "context" is one combination of choices made so far.
	contexts := []assignment{{}}
	for _, f := range order {
		var results [][]Option
		if len(parents.get(f)) == 0 {
			opts := rootOptions[f]
			for range contexts {
				results = append(results, opts)
			}
		} else {
			anc := fields[f].Parents
			keyOf := func(ctx assignment) string {
				vals := make([]string, len(anc))
				for i, a := range anc {
					vals[i] = ctx[a][0]
				}
				return strings.Join(vals, "\x00")
			}
			unique := map[string]assignment{}
			var keys []string
			for _, ctx := range contexts {
				k := keyOf(ctx)
				if _, ok := unique[k]; !ok {
					unique[k] = ctx
					keys = append(keys, k)
				}
			}
			fetchedBy := map[string][]Option{}
			total := len(keys)
			for start := 0; start < total; start += 60 {
				chunk := keys[start:min(start+60, total)]
				assigns := make([]assignment, len(chunk))
				for i, k := range chunk {
					assigns[i] = unique[k]
				}
				got := s.lookupBatch(templates[f], assigns)
				for i, k := range chunk {
					fetchedBy[k] = got[i]
				}
				if total > 60 {
					s.say(fmt.Sprintf("    %s: %d/%d lookups", f, min(start+60, total), total))
				}
			}
			for _, ctx := range contexts {
				results = append(results, fetchedBy[keyOf(ctx)])
			}
			for _, k := range keys {
				ctx := unique[k]
				chain := [][3]string{}
				for _, a := range anc {
					if vt, ok := ctx[a]; ok {
						chain = append(chain, [3]string{a, vt[0], vt[1]})
					}
				}
				rows = append(rows, RowResult{Field: f, Parents: chain, Options: nonNil(fetchedBy[k])})
			}
		}
		if len(children.get(f)) > 0 && !navigates[f] {
			var next []assignment
			for i, ctx := range contexts {
				for _, vt := range s.limit(results[i]) {
					n := assignment{}
					for k, v := range ctx {
						n[k] = v
					}
					n[f] = [2]string{vt[0], vt[1]}
					next = append(next, n)
				}
			}
			contexts = next
			if len(contexts) > 200000 {
				panic(errorf("Too many combinations below '%s' (%d) - stopping.", f, len(contexts)))
			}
		}
	}
	return rows
}

func (s *Scraper) lookupBatch(t *lookupTemplate, assigns []assignment) [][]Option {
	for attempt := 1; attempt <= 3; attempt++ {
		var out [][]Option
		err := try(func() { out = s.runLookups(t, assigns) })
		if err == nil {
			return out
		}
		var lost *errScrapeSessionLost
		if !(asErr(err, &lost) || isWD(err)) {
			panic(err)
		}
		s.say(fmt.Sprintf("    lookups failed (%s) - logging in again", short(err)))
		s.recoverSession()
		s.openPage(s.page)
	}
	panic(errorf("Lookups keep failing after logging in again."))
}

// --- every page ---------------------------------------------------------------------------

var nonWord = regexp.MustCompile(`\W+`)

func genericTabOpener(liID string) func(*Scraper) {
	return func(s *Scraper) {
		s.ensureTokenOpen()
		b := s.bot
		old := b.find(webdriver.ByTag("html"))
		b.jsClick(b.present(webdriver.ByXPath(fmt.Sprintf("//*[@id='%s']/a", liID)), defaultWait))
		b.untilQuiet(15, func() bool { return old.IsStale(b.ctx) })
		b.handleDialogs(0.5, 6)
		b.until(30, func() bool {
			v, _ := b.exec("return document.readyState").(string)
			return v == "complete"
		})
	}
}

// OtherTabs are the token tabs the scraper has no dedicated entry for
// (Executant Link, Enclosure, Stamp Paper, Note, ...).
func (s *Scraper) OtherTabs() []PageSpec {
	s.ensureTokenOpen()
	tabs, _ := s.js("return Array.from(document.querySelectorAll(\"li[id^='li_']\")).map(function (l) {" +
		" var a = l.querySelector('a'); return [l.id, a ? a.innerText.trim() : l.innerText.trim()]; });").([]any)
	var out []PageSpec
	for _, t := range tabs {
		pair, _ := t.([]any)
		if len(pair) != 2 {
			continue
		}
		liID, _ := pair[0].(string)
		text, _ := pair[1].(string)
		if knownTabIDs[liID] {
			continue
		}
		key := "tab_" + strings.ToLower(strings.Trim(nonWord.ReplaceAllString(liID[min(3, len(liID)):], "_"), "_"))
		label := text
		if label == "" {
			label = liID
		}
		out = append(out, PageSpec{Key: key, Label: label, Open: genericTabOpener(liID), NeedsToken: true})
	}
	return out
}

// Scrape reads the requested pages (all of them if pageKeys is empty).
func (s *Scraper) Scrape(pageKeys []string) *ScrapeResult {
	result := &ScrapeResult{Version: 1, ScrapedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00"),
		Pages: map[string]*PageResult{}, Failed: map[string]string{}}
	if s.token != "" {
		t := s.token
		result.Token = &t
	}
	s.bot.EnsureLoggedIn(10)
	var pages []PageSpec
	for _, p := range KnownPages {
		if !p.NeedsToken || s.token != "" {
			pages = append(pages, p)
		}
	}
	wantOther := len(pageKeys) == 0 || containsStr(pageKeys, "other_tabs")
	if s.token != "" && wantOther {
		var extra []PageSpec
		if err := try(func() { extra = s.OtherTabs() }); err != nil {
			if !isRaisedBroad(err) {
				panic(err)
			}
			result.Failed["other_tabs"] = short(err)
		}
		pages = append(pages, extra...)
	}
	for i := range pages {
		page := &pages[i]
		if len(pageKeys) > 0 && !containsStr(pageKeys, page.Key) &&
			!(strings.HasPrefix(page.Key, "tab_") && containsStr(pageKeys, "other_tabs")) {
			continue
		}
		s.say(fmt.Sprintf("Reading %s ...", page.Label))
		var pr *PageResult
		if err := try(func() { pr = s.scrapePage(page) }); err != nil {
			if errors_Is(err, errCanceled) {
				panic(err)
			}
			s.bot.log.Error("Scraping failed", "page", page.Key, "err", err)
			result.Failed[page.Key] = short(err)
			s.say(fmt.Sprintf("  %s failed: %s", page.Label, short(err)))
			_ = try(func() {
				if s.loggedOut() {
					s.recoverSession()
				}
			})
			continue
		}
		result.Pages[page.Key] = pr
		result.PageOrder = append(result.PageOrder, page.Key)
	}
	return result
}

func isRaisedBroad(err error) bool { return err != nil && !errors_Is(err, errCanceled) }
