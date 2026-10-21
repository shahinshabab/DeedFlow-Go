package web

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Obj is a template object: a map that prints as its __str__ entry, like a
// Django model instance prints as its __str__().
type Obj map[string]any

func (o Obj) String() string { s, _ := o["__str__"].(string); return s }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// viewer is who the request's user is for deed visibility.
func (c *Ctx) viewer() (store.Viewer, error) {
	return c.App.Store.ViewerFor(c.Context(), c.User.ID)
}

// deedOr404 loads the deed named by the <uuid:public_id> path value, or
// answers 404 (the uuid converter's and get_object_or_404's behavior).
func (c *Ctx) deedOr404() *models.Deed {
	id := strings.ToLower(c.PathValue("public_id"))
	if !uuidRe.MatchString(id) {
		c.NotFound()
		return nil
	}
	v, err := c.viewer()
	if err != nil {
		c.ServerError(err)
		return nil
	}
	d, err := c.App.Store.DeedByPublicID(c.Context(), v, id)
	if errors.Is(err, db.ErrNotFound) {
		c.NotFound()
		return nil
	}
	if err != nil {
		c.ServerError(err)
		return nil
	}
	return d
}

// deedObj is a deed as the templates see it (get_status_display, str, ...).
func deedObj(d *models.Deed, extra map[string]any) Obj {
	m := ObjectMap(d, map[string]any{
		"get_status_display": models.StatusLabel(d.Status),
		"__str__":            d.String(),
		"public_id":          d.PublicID,
	})
	for k, v := range extra {
		m[k] = v
	}
	return Obj(m)
}

// --- pagination (django.core.paginator.Paginator.get_page) -----------------------

// Pager works out one page of a counted list.
type Pager struct {
	Count, PerPage, Number, NumPages int
}

// NewPager clamps the requested page number like Paginator.get_page.
func NewPager(count, perPage int, requested string) Pager {
	pages := (count + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(requested))
	if err != nil || n < 1 {
		n = 1
	}
	if n > pages {
		n = pages
	}
	return Pager{Count: count, PerPage: perPage, Number: n, NumPages: pages}
}

// Offset is the number of rows before this page.
func (p Pager) Offset() int { return (p.Number - 1) * p.PerPage }

// Map is the page object the templates read.
func (p Pager) Map(objects any) map[string]any {
	start, end := 0, 0
	if p.Count > 0 {
		start = p.Offset() + 1
		end = min(p.Number*p.PerPage, p.Count)
	}
	return map[string]any{
		"object_list":          objects,
		"paginator":            map[string]any{"count": p.Count, "num_pages": p.NumPages},
		"number":               p.Number,
		"start_index":          start,
		"end_index":            end,
		"has_previous":         p.Number > 1,
		"has_next":             p.Number < p.NumPages,
		"previous_page_number": p.Number - 1,
		"next_page_number":     p.Number + 1,
	}
}

// queryWithout is the request's query string without one key, in its
// original order (QueryDict.urlencode()).
func queryWithout(raw, drop string) string {
	var parts []string
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		key, _, _ := strings.Cut(kv, "=")
		if k, err := url.QueryUnescape(key); err == nil && k == drop {
			continue
		}
		// Re-encode the way Python does (quote_plus on the decoded pair).
		k, v, _ := strings.Cut(kv, "=")
		dk, _ := url.QueryUnescape(k)
		dv, _ := url.QueryUnescape(v)
		parts = append(parts, url.QueryEscape(dk)+"="+url.QueryEscape(dv))
	}
	return strings.Join(parts, "&")
}

var dateRe = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)

// safeDate is parse_date(), with an invalid or impossible date giving nil.
func safeDate(s string) *time.Time {
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d || mo < 1 || y < 1 {
		return nil
	}
	return &t
}

var invalidFilenameChars = regexp.MustCompile(`[^-\p{L}\p{N}_.]`)

// validFilename is Django's get_valid_filename: spaces become underscores and
// anything but letters, digits, dashes, underscores and dots is dropped.
func validFilename(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), " ", "_")
	return invalidFilenameChars.ReplaceAllString(name, "")
}

func fmtFloat(f float64, places int) string { return fmt.Sprintf("%.*f", places, f) }
