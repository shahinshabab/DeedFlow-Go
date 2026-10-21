package web

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/media"
)

// Writing a form's instance to the database.

var reBadFilename = regexp.MustCompile(`[^\p{L}\p{N}_.\-]`)

// adminValidFilename is django.utils.text.get_valid_filename.
func adminValidFilename(name string) string {
	s := strings.ReplaceAll(strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/"))), " ", "_")
	return reBadFilename.ReplaceAllString(s, "")
}

// uploadName is the name an uploaded file is stored under (upload_to + name).
func (f *aForm) uploadName(field string, up *adminUpload, parentPK any) (string, error) {
	dir := ""
	switch f.m.Label + "." + field {
	case "deeds.deed.source_pdf":
		dir = "deed_pdfs/"
	case "deeds.deedscreenshot.image":
		dir = "deed_screenshots/"
	case "ledger.expensebill.file":
		var org any
		err := f.r.q().QueryRow(f.ctx, `SELECT organization_id FROM ledger_expense WHERE id = $1`, f.vals["expense"]).Scan(&org)
		if err != nil {
			return "", err
		}
		dir = fmt.Sprintf("expense_bills/%v/", org)
	}
	fn := adminValidFilename(up.Name)
	if fn == "" || fn == "." || fn == ".." {
		return "", fmt.Errorf("could not derive file name from %q", up.Name)
	}
	return media.Save(f.a.Cfg.MediaRoot, dir+fn, up.Data)
}

// dbValue converts a form value to the argument (and cast) the column takes.
func (f *aForm) dbValue(mf *modelFieldSpec, v any, parentPK any) (any, string, error) {
	switch x := v.(type) {
	case nil:
		return nil, "", nil
	case *adminUpload:
		name, err := f.uploadName(mf.Name, x, parentPK)
		return name, "", err
	case fileClear:
		return "", "", nil
	case jsonValue:
		b, err := jsonText(x.v)
		return b, "::jsonb", err
	}
	switch mf.InternalType {
	case "DecimalField":
		return valueString(v), "::numeric", nil
	case "UUIDField":
		return valueString(v), "::uuid", nil
	case "JSONField":
		return valueString(v), "::jsonb", nil
	}
	return v, "", nil
}

// saveInstance inserts or updates the form's instance. fk, when given, is
// the foreign key (of an inline) to point at parent.
func (f *aForm) saveInstance(fk string, parent any) (*adminObj, error) {
	if f.vals == nil {
		f.postCleanModel()
	}
	if fk != "" {
		f.vals[fk] = parent
	}
	vals := f.vals
	now := time.Now().In(f.a.Cfg.TimeZone)
	m := f.m
	insert := f.inst == nil
	// model save() hooks
	f.a.beforeSave(f, vals)
	var cols, phs []string
	var args []any
	for _, mf := range m.ModelFields {
		auto := mf.InternalType == "AutoField" || mf.InternalType == "BigAutoField"
		if mf.PrimaryKey && auto {
			continue
		}
		var val any
		var cast string
		switch {
		case mf.AutoNowAdd && insert, mf.AutoNow:
			val = now
		case mf.AutoNowAdd && !insert:
			continue
		default:
			var err error
			val, cast, err = f.dbValue(mf, vals[mf.Name], parent)
			if err != nil {
				return nil, err
			}
			if !insert {
				if _, inForm := f.fields[mf.Name]; !inForm {
					continue
				}
				if _, isUp := vals[mf.Name].(*adminUpload); !isUp && f.inst != nil && fmt.Sprint(f.inst.vals[mf.Name]) == fmt.Sprint(vals[mf.Name]) {
					continue
				}
			}
		}
		cols = append(cols, `"`+mf.Column+`"`)
		args = append(args, val)
		phs = append(phs, fmt.Sprintf("$%d%s", len(args), cast))
	}
	var pk any
	if insert {
		sql := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING "%s"`, quoteIdent(m.Table), strings.Join(cols, ", "), strings.Join(phs, ", "), m.pkField().Column)
		if err := f.r.q().QueryRow(f.ctx, sql, args...).Scan(&pk); err != nil {
			return nil, err
		}
	} else {
		pk = f.inst.vals[m.PK]
		if len(cols) > 0 {
			var sets []string
			for i, c := range cols {
				sets = append(sets, c+" = "+phs[i])
			}
			args = append(args, pk)
			sql := fmt.Sprintf(`UPDATE %s SET %s WHERE "%s" = $%d`, quoteIdent(m.Table), strings.Join(sets, ", "), m.pkField().Column, len(args))
			if _, err := f.r.q().Exec(f.ctx, sql, args...); err != nil {
				return nil, err
			}
		}
	}
	// many-to-many fields
	for _, n := range f.order {
		fl := f.fields[n]
		if fl == nil || fl.mf == nil || !fl.mf.isM2M() || !f.cleanedOK[n] {
			continue
		}
		ids, _ := f.cleaned[n].([]int64)
		if err := f.saveM2M(fl.mf, pk, ids); err != nil {
			return nil, err
		}
	}
	// reload the saved row
	saved, err := f.a.loadObjTx(f.r, m, valueString(pk))
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, fmt.Errorf("admin: saved %s %v not found", m.Label, pk)
	}
	f.inst = saved
	return saved, nil
}

func (f *aForm) saveM2M(mf *modelFieldSpec, pk any, ids []int64) error {
	through := f.m.Table + "_" + mf.Name
	left := f.m.ModelName + "_id"
	right := mf.Related.ModelName + "_id"
	q := f.r.q()
	if _, err := q.Exec(f.ctx, fmt.Sprintf(`DELETE FROM %s WHERE "%s" = $1`, quoteIdent(through), left), pk); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := q.Exec(f.ctx, fmt.Sprintf(`INSERT INTO %s ("%s", "%s") VALUES ($1, $2)`, quoteIdent(through), left, right), pk, id); err != nil {
			return err
		}
	}
	return nil
}

// loadObjTx is loadObj through the request's transaction.
func (a *App) loadObjTx(r *adminReq, m *modelSpec, pk string) (*adminObj, error) {
	arg, ok := coerceValue(m.pkField(), pk)
	if !ok {
		return nil, nil
	}
	rows, err := r.q().Query(r.c.Context(), fmt.Sprintf(`SELECT %s FROM %s t WHERE t."%s" = $1`,
		m.selectList("t"), quoteIdent(m.Table), m.pkField().Column), arg)
	if err != nil {
		return nil, err
	}
	objs, err := a.scanObjs(m, rows)
	if err != nil || len(objs) == 0 {
		return nil, err
	}
	return objs[0], nil
}

// beforeSave runs what the models' save() methods do.
func (a *App) beforeSave(f *aForm, vals map[string]any) {
	switch f.m.Label {
	case "deeds.deed":
		// a deed without an organization takes its owner's
		if vals["organization"] == nil && vals["owner"] != nil {
			var org *int64
			err := f.r.q().QueryRow(f.ctx, `SELECT organization_id FROM accounts_membership WHERE user_id = $1 ORDER BY id LIMIT 1`, vals["owner"]).Scan(&org)
			if err == nil && org != nil {
				vals["organization"] = *org
			}
		}
	}
}

// deleteObjects deletes objects with everything that cascades from them.
func (a *App) deleteObjects(r *adminReq, objs []*adminObj) error {
	col := a.newCollector(r.c.Context())
	col.q = r.q
	if err := col.collect(objs, ""); err != nil {
		return err
	}
	return a.deleteWith(r, col)
}

// jsonText encodes a parsed JSON value for the jsonb column.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// pyASCII escapes non-ASCII text the way json.dumps(ensure_ascii=True) does.
func pyASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return b.String()
}

// pyString is json.dumps(str) (ensure_ascii).
func pyString(s string) string {
	b, _ := json.Marshal(s)
	out := string(b)
	// Go escapes <, > and & which Python does not
	out = strings.NewReplacer(`<`, "<", `>`, ">", `&`, "&", ` `, " ", ` `, " ").Replace(out)
	return pyASCII(out)
}
