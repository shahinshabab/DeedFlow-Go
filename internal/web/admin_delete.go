package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/flosch/pongo2/v6"
	"github.com/jackc/pgx/v5"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Deleting objects the way the admin does: a walk over everything that would
// be deleted with them (django.contrib.admin.utils.NestedObjects, built on the
// deletion Collector), the confirmation page, and the deletion itself.

type delKey struct {
	label string
	pk    string
}

func objKey(o *adminObj) delKey { return delKey{o.m.Label, o.pkString()} }

// delCollector collects objects and the edges between them.
type delCollector struct {
	a   *App
	ctx context.Context
	q   func() db.Querier

	edges      map[delKey][]*adminObj // parent -> children, in the order found
	roots      []*adminObj
	data       map[string][]*adminObj // label -> objects to delete, in order found
	seen       map[delKey]bool
	modelOrder []string
	modelObjs  map[string]map[string]bool
	updates    []delUpdate // SET_NULL updates
}

type delUpdate struct {
	table, column string
	pkCol         string
	ids           []any
}

func (a *App) newCollector(ctx context.Context) *delCollector {
	return &delCollector{a: a, ctx: ctx, q: a.Store.Querier, edges: map[delKey][]*adminObj{}, data: map[string][]*adminObj{},
		seen: map[delKey]bool{}, modelObjs: map[string]map[string]bool{}}
}

// collect is Collector.collect as NestedObjects overrides it. source is the
// model whose objects these cascade from (its field is sourceAttr).
func (c *delCollector) collect(objs []*adminObj, sourceAttr string) error {
	for _, o := range objs {
		if sourceAttr != "" {
			g := adminSiteSpec.graph[o.m.Label]
			var rel *modelFieldSpec
			if g != nil {
				rel = o.m.byName[sourceAttr]
			}
			if rel == nil || rel.Related == nil {
				return fmt.Errorf("admin: no relation %s on %s", sourceAttr, o.m.Label)
			}
			pk := valueString(o.vals[sourceAttr])
			parent := delKey{rel.Related.Label, pk}
			c.edges[parent] = append(c.edges[parent], o)
		} else {
			c.roots = append(c.roots, o)
		}
		if c.modelObjs[o.m.Label] == nil {
			c.modelObjs[o.m.Label] = map[string]bool{}
			c.modelOrder = append(c.modelOrder, o.m.Label)
		}
		c.modelObjs[o.m.Label][o.pkString()] = true
	}
	// add(): only objects not collected before go on
	var fresh []*adminObj
	for _, o := range objs {
		k := objKey(o)
		if c.seen[k] {
			continue
		}
		c.seen[k] = true
		c.data[o.m.Label] = append(c.data[o.m.Label], o)
		fresh = append(fresh, o)
	}
	if len(fresh) == 0 {
		return nil
	}
	g := adminSiteSpec.graph[fresh[0].m.Label]
	if g == nil {
		return nil
	}
	for _, rel := range g.Relations {
		rg := adminSiteSpec.graph[rel.Model]
		if rg == nil {
			continue
		}
		ids := make([]any, len(fresh))
		for i, o := range fresh {
			ids[i] = o.vals[o.m.PK]
		}
		order := ""
		if len(rg.Ordering) > 0 {
			var parts []string
			for _, o := range rg.Ordering {
				p := `t."` + o.Column + `"`
				if o.Desc {
					p += " DESC"
				}
				parts = append(parts, p)
			}
			order = " ORDER BY " + strings.Join(parts, ", ")
		}
		rows, err := c.q().Query(c.ctx, fmt.Sprintf(`SELECT %s FROM %s t WHERE t."%s" = ANY($1)%s`,
			rg.spec.selectList("t"), quoteIdent(rg.Table), rel.Column, order), ids)
		if err != nil {
			return err
		}
		sub, err := c.a.scanObjs(rg.spec, rows)
		if err != nil {
			return err
		}
		if len(sub) == 0 {
			continue
		}
		switch rel.OnDelete {
		case "CASCADE":
			if err := c.collect(sub, rel.Field); err != nil {
				return err
			}
		case "SET_NULL":
			u := delUpdate{table: rg.Table, column: rel.Column, pkCol: rg.PKColumn}
			for _, s := range sub {
				u.ids = append(u.ids, s.vals[s.m.PK])
			}
			c.updates = append(c.updates, u)
		case "DO_NOTHING":
		default:
			return fmt.Errorf("admin: unsupported on_delete %s for %s.%s", rel.OnDelete, rel.Model, rel.Field)
		}
	}
	return nil
}

// nested is NestedObjects.nested.
func (c *delCollector) nested(format func(*adminObj) string) []any {
	seen := map[delKey]bool{}
	var walk func(o *adminObj) []any
	walk = func(o *adminObj) []any {
		k := objKey(o)
		if seen[k] {
			return nil
		}
		seen[k] = true
		var children []any
		for _, ch := range c.edges[k] {
			children = append(children, walk(ch)...)
		}
		ret := []any{format(o)}
		if len(children) > 0 {
			ret = append(ret, children)
		}
		return ret
	}
	var out []any
	for _, r := range c.roots {
		out = append(out, walk(r)...)
	}
	return out
}

// deletion is what get_deleted_objects returns.
type deletion struct {
	nested     []any
	counts     [][]any
	permsLack  []string
	protected  []string
	collector  *delCollector
	displayStr map[delKey]string
}

// getDeletedObjects is django.contrib.admin.utils.get_deleted_objects.
func (a *App) getDeletedObjects(r *adminReq, objs []*adminObj) (*deletion, error) {
	d := &deletion{}
	if len(objs) == 0 {
		return d, nil
	}
	ctx := r.c.Context()
	col := a.newCollector(ctx)
	if err := col.collect(objs, ""); err != nil {
		return nil, err
	}
	d.collector = col
	re := a.newRepr(ctx)
	lack := map[string]bool{}
	format := func(o *adminObj) string {
		g := adminSiteSpec.graph[o.m.Label]
		name := capfirst(g.VerboseName)
		if reg := adminSiteSpec.models[o.m.Label]; reg != nil {
			if !r.perms.has(reg, "delete") {
				lack[g.VerboseName] = true
			}
			href := a.adminURL(reg, "change", adminQuote(o.pkString()))
			return esc(name) + `: <a href="` + esc(href) + `">` + esc(re.str(o)) + `</a>`
		}
		return esc(name + ": " + re.str(o))
	}
	d.nested = col.nested(format)
	seen := map[string]int{}
	for _, label := range col.modelOrder {
		g := adminSiteSpec.graph[label]
		n := len(col.modelObjs[label])
		if i, ok := seen[g.VerboseNamePlural]; ok {
			d.counts[i][1] = n
			continue
		}
		seen[g.VerboseNamePlural] = len(d.counts)
		d.counts = append(d.counts, []any{g.VerboseNamePlural, n})
	}
	for k := range lack {
		d.permsLack = append(d.permsLack, k)
	}
	sort.Strings(d.permsLack)
	return d, nil
}

// deleteCollected deletes what the collector found (Collector.delete):
// SET_NULL updates first, then the rows, children before parents.
func (a *App) deleteCollected(ctx context.Context, col *delCollector, q db.Querier) error {
	for _, u := range col.updates {
		if _, err := q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET "%s" = NULL WHERE "%s" = ANY($1)`,
			quoteIdent(u.table), u.column, u.pkCol), u.ids); err != nil {
			return err
		}
	}
	for i := len(col.modelOrder) - 1; i >= 0; i-- {
		label := col.modelOrder[i]
		g := adminSiteSpec.graph[label]
		var ids []any
		for _, o := range col.data[label] {
			ids = append(ids, o.vals[o.m.PK])
		}
		if len(ids) == 0 {
			continue
		}
		if _, err := q.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE "%s" = ANY($1)`, quoteIdent(g.Table), g.PKColumn), ids); err != nil {
			return err
		}
	}
	return nil
}

// withTx runs fn in a transaction (the request's queries then go through it).
func (a *App) withTx(r *adminReq, fn func() error) error {
	if r.tx != nil {
		return fn()
	}
	return a.Store.InTx(r.c.Context(), func(tx *store.Store) error {
		r.tx = tx
		defer func() { r.tx = nil }()
		return fn()
	})
}

func (a *App) deleteWith(r *adminReq, col *delCollector) error {
	return a.deleteCollected(r.c.Context(), col, r.q())
}

var _ = pgx.ErrNoRows

// deleteSelected is the delete_selected action.
func (a *App) deleteSelected(r *adminReq, cl *changeList, selected []string, across bool) (bool, error) {
	c := r.c
	m := cl.m
	ctx := c.Context()
	// the selected objects among those the changelist shows
	q, err := cl.buildQuery(nil)
	if err != nil {
		return false, err
	}
	if !across {
		var ids []any
		for _, s := range selected {
			arg, ok := coerceValue(m.pkField(), s)
			if !ok {
				continue
			}
			ids = append(ids, arg)
		}
		q.where = append(q.where, fmt.Sprintf(`t."%s" = ANY(%s)`, m.pkField().Column, q.arg(ids)))
	}
	rows, err := a.Store.Querier().Query(ctx, "SELECT "+m.selectList("t")+q.fromWhere()+q.orderLimit(0, 0), q.args...)
	if err != nil {
		return false, err
	}
	objs, err := a.scanObjs(m, rows)
	if err != nil {
		return false, err
	}
	d, err := a.getDeletedObjects(r, objs)
	if err != nil {
		return false, err
	}
	if c.PostForm().Get("post") != "" && len(d.protected) == 0 {
		if len(d.permsLack) > 0 {
			a.adminForbidden(c)
			return true, nil
		}
		n := len(objs)
		if n > 0 {
			re := a.newRepr(ctx)
			for _, o := range objs {
				if err := a.logAction(ctx, c.User.ID, m, o.pkString(), re.str(o), logDeletion, ""); err != nil {
					return false, err
				}
			}
			if err := a.withTx(r, func() error { return a.deleteCollected(ctx, d.collector, r.q()) }); err != nil {
				return false, err
			}
			items := m.VerboseName
			if n != 1 {
				items = m.VerboseNamePlural
			}
			c.Session.addMessage("success", fmt.Sprintf("Successfully deleted %d %s.", n, items))
		}
		c.SaveSession()
		c.Redirect(c.R.URL.RequestURI())
		return true, nil
	}
	objectsName := m.VerboseName
	if len(objs) != 1 {
		objectsName = m.VerboseNamePlural
	}
	title := "Are you sure?"
	if len(d.permsLack) > 0 || len(d.protected) > 0 {
		title = "Cannot delete " + objectsName
	}
	ctxMap := r.base(title)
	ctxMap["objects_name"] = objectsName
	ctxMap["deletable_objects"] = []any{d.nested}
	ctxMap["model_count"] = d.counts
	qs := make([]any, len(objs))
	for i, o := range objs {
		qs[i] = map[string]any{"pk": o.pkString()}
	}
	ctxMap["queryset"] = qs
	ctxMap["perms_lacking"] = stringsToAny(d.permsLack)
	ctxMap["protected"] = stringsToAny(d.protected)
	ctxMap["opts"] = a.adminOpts(m)
	ctxMap["action_checkbox_name"] = "_selected_action"
	ctxMap["media"] = adminMedia(false)["js"]
	c.renderContext(http.StatusOK, "admin/delete_selected_confirmation.html", ctxMap)
	return true, nil
}

func stringsToAny(s []string) []any {
	if len(s) == 0 {
		return nil
	}
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

var _ = pongo2.Context{}
