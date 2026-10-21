package web

import (
	"fmt"
	"strings"
)

// The changelist's bulk actions: delete_selected is the only one the project's
// model admins have.

type adminAction struct {
	name        string
	description string // with %(verbose_name_plural)s already filled in
}

// adminActions are the actions the user may run on the model's objects.
func (a *App) adminActions(r *adminReq, m *modelSpec) []adminAction {
	var out []adminAction
	for _, name := range m.Actions {
		if name == "delete_selected" && r.perms.has(m, "delete") {
			out = append(out, adminAction{name: name, description: "Delete selected " + m.VerboseNamePlural})
		}
	}
	return out
}

// actionForm is the ActionForm the changelist prints above the table.
func actionForm(actions []adminAction, m *modelSpec, a *App) []any {
	var sel strings.Builder
	sel.WriteString("<select name=\"action\" required>\n  <option value=\"\" selected>---------</option>\n")
	for _, ac := range actions {
		sel.WriteString("\n  <option value=\"" + esc(ac.name) + "\">" + esc(ac.description) + "</option>\n")
	}
	sel.WriteString("\n</select>")
	action := newBoundField("action", "Action:", true, sel.String(), nil, "")
	across := newBoundField("select_across", "", false, `<input type="hidden" name="select_across" value="0" class="select-across">`, nil, "")
	return []any{action, across}
}

// runAdminAction handles a POST of the changelist's action form. handled is
// true when a response was written.
func (a *App) runAdminAction(r *adminReq, cl *changeList, actions []adminAction) (bool, error) {
	c := r.c
	pf := c.PostForm()
	name := ""
	idx := 0
	if v := pf.Get("index"); v != "" {
		fmt.Sscanf(v, "%d", &idx)
	}
	if list := pf["action"]; idx >= 0 && idx < len(list) {
		name = list[idx]
	}
	var act *adminAction
	for i := range actions {
		if actions[i].name == name {
			act = &actions[i]
		}
	}
	if act == nil {
		c.Session.addMessage("warning", "No action selected.")
		return false, nil
	}
	selectAcross := pf.Get("select_across") != "" && pf.Get("select_across") != "0" && strings.ToLower(pf.Get("select_across")) != "false"
	selected := pf["_selected_action"]
	if len(selected) == 0 && !selectAcross {
		c.Session.addMessage("warning", "Items must be selected in order to perform actions on them. No items have been changed.")
		return false, nil
	}
	switch act.name {
	case "delete_selected":
		return a.deleteSelected(r, cl, selected, selectAcross)
	}
	return false, nil
}
