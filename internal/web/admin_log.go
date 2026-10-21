package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The admin's log of actions (django_admin_log): what the index page's
// "Recent actions" and each object's history page show.

const (
	logAddition = 1
	logChange   = 2
	logDeletion = 3
)

var (
	ctypeOnce  sync.Once
	ctypeByKey map[string]int64
	ctypeByID  map[int64]string
)

// contentTypeID is the id of a model's row in django_content_type.
func (a *App) contentTypeID(ctx context.Context, label string) (int64, bool) {
	ctypeOnce.Do(func() {
		ctypeByKey, ctypeByID = map[string]int64{}, map[int64]string{}
		rows, err := a.Store.Querier().Query(ctx, `SELECT id, app_label, model FROM django_content_type`)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var app, model string
			if rows.Scan(&id, &app, &model) == nil {
				ctypeByKey[app+"."+model] = id
				ctypeByID[id] = app + "." + model
			}
		}
	})
	id, ok := ctypeByKey[label]
	return id, ok
}

func (a *App) contentTypeLabel(ctx context.Context, id int64) string {
	a.contentTypeID(ctx, "")
	return ctypeByID[id]
}

// logAction writes a LogEntry. changeMessage is the JSON list Django stores.
func (a *App) logAction(ctx context.Context, userID int64, m *modelSpec, objectID, repr string, flag int, changeMessage string) error {
	ct, ok := a.contentTypeID(ctx, m.Label)
	var ctArg any
	if ok {
		ctArg = ct
	}
	if len([]rune(repr)) > 200 {
		repr = string([]rune(repr)[:200])
	}
	_, err := a.Store.Querier().Exec(ctx, `INSERT INTO django_admin_log
		(action_time, user_id, content_type_id, object_id, object_repr, action_flag, change_message)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, time.Now(), userID, ctArg, objectID, repr, flag, changeMessage)
	return err
}

// logEntry is a row of the log.
type logEntry struct {
	ID            int64
	Time          time.Time
	UserID        int64
	Username      string
	CTLabel       string
	ObjectID      *string
	ObjectRepr    string
	Flag          int
	ChangeMessage string
}

func (a *App) scanLog(ctx context.Context, where string, args []any, limit int) ([]logEntry, error) {
	q := `SELECT l.id, l.action_time, l.user_id, u.username, l.content_type_id, l.object_id, l.object_repr, l.action_flag, l.change_message
		FROM django_admin_log l JOIN auth_user u ON u.id = l.user_id ` + where + ` ORDER BY l.action_time DESC, l.id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := a.Store.Querier().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []logEntry
	for rows.Next() {
		var e logEntry
		var ct *int64
		var flag int16
		if err := rows.Scan(&e.ID, &e.Time, &e.UserID, &e.Username, &ct, &e.ObjectID, &e.ObjectRepr, &flag, &e.ChangeMessage); err != nil {
			return nil, err
		}
		e.Flag = int(flag)
		if ct != nil {
			e.CTLabel = a.contentTypeLabel(ctx, *ct)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// adminURL is LogEntry.get_admin_url.
func (a *App) logEntryURL(e logEntry) string {
	if e.CTLabel == "" || e.ObjectID == nil {
		return ""
	}
	m := adminSiteSpec.models[e.CTLabel]
	if m == nil {
		return ""
	}
	return a.adminURL(m, "change", adminQuote(*e.ObjectID))
}

// entryMap is a log entry as the templates read it.
func (a *App) entryMap(ctx context.Context, e logEntry) map[string]any {
	m := map[string]any{
		"is_addition": e.Flag == logAddition, "is_change": e.Flag == logChange, "is_deletion": e.Flag == logDeletion,
		"object_repr": e.ObjectRepr, "get_admin_url": a.logEntryURL(e), "action_time": e.Time,
		"get_change_message": logChangeMessage(e.ChangeMessage), "content_type": nil,
		"user": map[string]any{"get_username": e.Username, "username": e.Username},
	}
	if e.CTLabel != "" {
		name := e.CTLabel
		if am := adminSiteSpec.any[e.CTLabel]; am != nil {
			name = am.VerboseName
		}
		m["content_type"] = map[string]any{"name": name}
	}
	return m
}

func (r *adminReq) recentActions(n int) []any {
	entries, err := r.a.scanLog(r.c.Context(), "WHERE l.user_id = $1", []any{r.c.User.ID}, n)
	if err != nil {
		r.a.Log.Error("admin log", "err", err)
		return nil
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = r.a.entryMap(r.c.Context(), e)
	}
	return out
}

// textList is Django's get_text_list(list, "and").
func textList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// logChangeMessage is LogEntry.get_change_message.
func logChangeMessage(raw string) string {
	if raw == "" || raw[0] != '[' {
		return raw
	}
	var parts []map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return raw
	}
	var msgs []string
	for _, p := range parts {
		switch {
		case p["added"] != nil || hasKey(p, "added"):
			a := p["added"]
			if len(a) > 0 {
				msgs = append(msgs, fmt.Sprintf("Added %v “%v”.", a["name"], a["object"]))
			} else {
				msgs = append(msgs, "Added.")
			}
		case hasKey(p, "changed"):
			c := p["changed"]
			var fields []string
			if fl, ok := c["fields"].([]any); ok {
				for _, f := range fl {
					fields = append(fields, fmt.Sprint(f))
				}
			}
			if _, ok := c["name"]; ok {
				msgs = append(msgs, fmt.Sprintf("Changed %s for %v “%v”.", textList(fields), c["name"], c["object"]))
			} else {
				msgs = append(msgs, fmt.Sprintf("Changed %s.", textList(fields)))
			}
		case hasKey(p, "deleted"):
			d := p["deleted"]
			msgs = append(msgs, fmt.Sprintf("Deleted %v “%v”.", d["name"], d["object"]))
		}
	}
	for i, m := range msgs {
		if m != "" {
			r := []rune(m)
			r[0] = []rune(strings.ToUpper(string(r[0])))[0]
			msgs[i] = string(r)
		}
	}
	out := strings.Join(msgs, " ")
	if out == "" {
		return "No fields changed."
	}
	return out
}

func hasKey(m map[string]map[string]any, k string) bool { _, ok := m[k]; return ok }
