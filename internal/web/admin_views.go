package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"
)

// The history page and the delete confirmation page of one object.

// adminHistory is ModelAdmin.history_view.
func (a *App) adminHistory(r *adminReq, m *modelSpec) {
	c := r.c
	id := adminUnquote(c.PathValue("object_id"))
	obj, err := a.loadObj(c.Context(), m, id)
	if err != nil {
		c.ServerError(err)
		return
	}
	if obj == nil {
		a.objectMissing(r, m, c.PathValue("object_id"))
		return
	}
	if !r.perms.has(m, "view") && !r.perms.has(m, "change") {
		a.adminForbiddenPage(c)
		return
	}
	ct, _ := a.contentTypeID(c.Context(), m.Label)
	rows, err := r.q().Query(c.Context(), `SELECT l.action_time, u.username, u.first_name, u.last_name, l.change_message, l.action_flag, l.object_repr
		FROM django_admin_log l JOIN auth_user u ON u.id = l.user_id
		WHERE l.object_id = $1 AND l.content_type_id = $2 ORDER BY l.action_time, l.id`, id, ct)
	if err != nil {
		c.ServerError(err)
		return
	}
	var all []map[string]any
	for rows.Next() {
		var t time.Time
		var u, fn, ln, msg, repr string
		var flag int16
		if err := rows.Scan(&t, &u, &fn, &ln, &msg, &flag, &repr); err != nil {
			rows.Close()
			c.ServerError(err)
			return
		}
		full := strings.TrimSpace(fn + " " + ln)
		all = append(all, map[string]any{
			"action_time": t.In(a.Cfg.TimeZone), "user": map[string]any{"get_username": u, "get_full_name": full},
			"get_change_message": historyMessage(int(flag), repr, msg),
		})
	}
	rows.Close()
	perPage := 100
	count := len(all)
	numPages := (count + perPage - 1) / perPage
	if numPages < 1 {
		numPages = 1
	}
	pageNum := 1
	if v := c.R.URL.Query().Get("p"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			pageNum = n
		}
	}
	if pageNum < 1 {
		pageNum = 1
	}
	if pageNum > numPages {
		pageNum = numPages
	}
	lo, hi := (pageNum-1)*perPage, pageNum*perPage
	if hi > count {
		hi = count
	}
	var pageEntries []any
	for _, e := range all[lo:hi] {
		pageEntries = append(pageEntries, e)
	}
	re := a.newRepr(c.Context())
	ctx := r.base("Change history: " + re.str(obj))
	ctx["action_list"] = pageEntries
	ctx["action_count"] = count
	ctx["page_var"] = pageVar
	ctx["pagination_required"] = count > perPage
	cl := &changeList{numPages: numPages, pageNum: pageNum}
	var rng []any
	if count > perPage {
		for _, p := range cl.pageRange() {
			rng = append(rng, p)
		}
	}
	ctx["page_range"] = rng
	ctx["module_name"] = capfirst(m.VerboseNamePlural)
	ctx["object_repr"] = re.str(obj)
	ctx["object_pk"] = obj.pkString()
	ctx["opts"] = a.adminOpts(m)
	ctx["preserved_filters"] = (&cfView{r: r}).preservedFilters()
	r.render("admin/object_history.html", ctx)
}

// historyMessage is LogEntry.get_change_message.
func historyMessage(flag int, repr, raw string) string {
	switch {
	case raw == "" && flag == logAddition:
		return "Added."
	case raw == "" && flag == logChange:
		return "No fields changed."
	case raw == "" && flag == logDeletion:
		return "Deleted."
	}
	return logChangeMessage(raw)
}

// adminDelete is ModelAdmin.delete_view.
func (a *App) adminDelete(r *adminReq, m *modelSpec) {
	c := r.c
	rawID := c.PathValue("object_id")
	id := adminUnquote(rawID)
	toField := c.R.URL.Query().Get("_to_field")
	if c.R.Method == http.MethodPost && c.PostForm().Get("_to_field") != "" {
		toField = c.PostForm().Get("_to_field")
	}
	if toField != "" && toField != m.PK && toField != "pk" {
		a.adminBadRequest(c)
		return
	}
	obj, err := a.loadObj(c.Context(), m, id)
	if err != nil {
		c.ServerError(err)
		return
	}
	if !r.perms.has(m, "delete") {
		a.adminForbiddenPage(c)
		return
	}
	if obj == nil {
		a.objectMissing(r, m, rawID)
		return
	}
	d, err := a.getDeletedObjects(r, []*adminObj{obj})
	if err != nil {
		c.ServerError(err)
		return
	}
	re := a.newRepr(c.Context())
	objRepr := re.str(obj)
	if c.R.Method == http.MethodPost && c.PostForm().Get("post") != "" || c.R.Method == http.MethodPost && len(c.PostForm()) > 0 {
		if len(d.protected) == 0 {
			if len(d.permsLack) > 0 {
				a.adminForbiddenPage(c)
				return
			}
			err := a.withTx(r, func() error {
				if err := a.logAction(c.Context(), c.User.ID, m, obj.pkString(), objRepr, logDeletion, ""); err != nil {
					return err
				}
				return a.deleteCollected(c.Context(), d.collector, r.q())
			})
			if err != nil {
				c.ServerError(err)
				return
			}
			if _, popup := c.PostForm()["_popup"]; popup {
				c.renderContext(http.StatusOK, "admin/popup_response.html", pongo2.Context{
					"popup_response_data": fmt.Sprintf(`{"action": "delete", "value": %s}`, pyString(obj.pkString()))})
				return
			}
			c.Session.addMessage("success", fmt.Sprintf("The %s “%s” was deleted successfully.", m.VerboseName, objRepr))
			idx, _ := a.Router.Reverse("admin:index")
			dest := idx
			if r.perms.has(m, "change") {
				dest = addPreservedFilters(a.adminURL(m, "changelist"), m, (&cfView{r: r}).preservedFilters(), a)
			}
			c.SaveSession()
			c.Redirect(dest)
			return
		}
	}
	title := "Are you sure?"
	if len(d.permsLack) > 0 || len(d.protected) > 0 {
		title = "Cannot delete " + m.VerboseName
	}
	ctx := r.base(title)
	ctx["object_name"] = m.VerboseName
	ctx["object_repr"] = objRepr
	ctx["object_pk"] = obj.pkString()
	ctx["deleted_objects"] = d.nested
	ctx["model_count"] = d.counts
	ctx["perms_lacking"] = stringsToAny(d.permsLack)
	ctx["protected"] = stringsToAny(d.protected)
	ctx["opts"] = a.adminOpts(m)
	ctx["app_label"] = m.AppLabel
	ctx["preserved_filters"] = (&cfView{r: r}).preservedFilters()
	_, popup := c.R.URL.Query()["_popup"]
	ctx["is_popup"] = popup
	ctx["to_field"] = nil
	if toField != "" {
		ctx["to_field"] = toField
	}
	ctx["to_field_var"] = toFieldVr
	ctx["is_popup_var"] = popupVar
	ctx["media"] = adminMedia(false)["js"]
	r.render("admin/delete_confirmation.html", ctx)
}
