package web

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// The admin site is driven by a description of the Django project's admin
// (admin_spec.json, written by tools/admin/gen_spec.py): its models, model
// admins, forms and inlines.

//go:embed admin_spec.json
var adminSpecJSON []byte

type adminSpecFile struct {
	Apps      map[string]string `json:"apps"`
	AppNames  map[string]string `json:"app_names"`
	AllModels []*adminAnyModel  `json:"all_models"`
	Models    []*modelSpec      `json:"models"`
}

// adminAnyModel is any installed model (for permission and content type names).
type adminAnyModel struct {
	Label             string `json:"label"`
	AppLabel          string `json:"app_label"`
	ModelName         string `json:"model_name"`
	VerboseName       string `json:"verbose_name"`
	VerboseNamePlural string `json:"verbose_name_plural"`
	AppVerboseName    string `json:"app_verbose_name"`
	Table             string `json:"table"`
}

type modelFieldSpec struct {
	Name         string      `json:"name"`
	Column       string      `json:"column"`
	Attname      string      `json:"attname"`
	InternalType string      `json:"internal_type"`
	VerboseName  string      `json:"verbose_name"`
	HelpText     string      `json:"help_text"`
	Null         bool        `json:"null"`
	Blank        bool        `json:"blank"`
	Editable     bool        `json:"editable"`
	PrimaryKey   bool        `json:"primary_key"`
	Unique       bool        `json:"unique"`
	MaxLength    *int        `json:"max_length"`
	AutoNow      bool        `json:"auto_now"`
	AutoNowAdd   bool        `json:"auto_now_add"`
	HasDefault   bool        `json:"has_default"`
	Default      any         `json:"default"`
	Choices      [][]string  `json:"choices"`
	ChoiceValues []any       `json:"choice_values"`
	MaxDigits    int         `json:"max_digits"`
	DecimalPlace int         `json:"decimal_places"`
	Related      *relatedRef `json:"related"`
	Validators   []string    `json:"validators"`
}

type relatedRef struct {
	Label       string   `json:"label"`
	AppLabel    string   `json:"app_label"`
	ModelName   string   `json:"model_name"`
	VerboseName string   `json:"verbose_name"`
	Table       string   `json:"table"`
	ToField     string   `json:"to_field"`
	OneToOne    bool     `json:"one_to_one"`
	ManyToMany  bool     `json:"many_to_many"`
	OnDelete    string   `json:"on_delete"`
	Ordering    []string `json:"ordering"`
}

type widgetSpec struct {
	Class          string         `json:"class"`
	Attrs          map[string]any `json:"attrs"`
	InputType      string         `json:"input_type"`
	Inner          *widgetSpec    `json:"inner"`
	CanAddRelated  bool           `json:"can_add_related"`
	CanChangeRel   bool           `json:"can_change_related"`
	CanDeleteRel   bool           `json:"can_delete_related"`
	CanViewRelated bool           `json:"can_view_related"`
	ModelName      string         `json:"model_name"`
	RelatedLabel   string         `json:"related_label"`
	Choices        [][]string     `json:"choices"`
	VerboseName    string         `json:"verbose_name"`
	IsStacked      bool           `json:"is_stacked"`
}

type querysetSpec struct {
	Label      string   `json:"label"`
	Table      string   `json:"table"`
	Ordering   []string `json:"ordering"`
	ToField    *string  `json:"to_field"`
	EmptyLabel *string  `json:"empty_label"`
}

type formFieldSpec struct {
	Name         string          `json:"name"`
	FieldClass   string          `json:"field_class"`
	Label        *string         `json:"label"`
	Required     bool            `json:"required"`
	HelpText     string          `json:"help_text"`
	Disabled     bool            `json:"disabled"`
	Widget       widgetSpec      `json:"widget"`
	Initial      any             `json:"initial"`
	MaxLength    *int            `json:"max_length"`
	MinLength    *int            `json:"min_length"`
	MaxValue     any             `json:"max_value"`
	MinValue     any             `json:"min_value"`
	MaxDigits    *int            `json:"max_digits"`
	DecimalPlace *int            `json:"decimal_places"`
	EmptyValue   any             `json:"empty_value"`
	Strip        *bool           `json:"strip"`
	Queryset     *querysetSpec   `json:"queryset"`
	Multiple     bool            `json:"multiple"`
	Choices      [][]string      `json:"choices"`
	Coerce       string          `json:"coerce"`
	ModelField   *modelFieldSpec `json:"model_field"`
}

type listDisplaySpec struct {
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	Boolean          bool            `json:"boolean"`
	ShortDescription string          `json:"short_description"`
	AdminOrderField  *string         `json:"admin_order_field"`
	Field            *modelFieldSpec `json:"field"`
}

type inlineSpec struct {
	Class          string                    `json:"class"`
	Template       string                    `json:"template"`
	Model          string                    `json:"model"`
	ModelName      string                    `json:"model_name"`
	AppLabel       string                    `json:"app_label"`
	VerboseName    string                    `json:"verbose_name"`
	VerboseNamePl  string                    `json:"verbose_name_plural"`
	Table          string                    `json:"table"`
	FKName         string                    `json:"fk_name"`
	FKColumn       string                    `json:"fk_column"`
	OneToOne       bool                      `json:"one_to_one"`
	Extra          int                       `json:"extra"`
	MinNum         int                       `json:"min_num"`
	MaxNum         *int                      `json:"max_num"`
	CanDelete      bool                      `json:"can_delete"`
	ShowChangeLink bool                      `json:"show_change_link"`
	Fields         []string                  `json:"fields"`
	FormFields     map[string]*formFieldSpec `json:"form_fields"`
	ReadonlyFields []string                  `json:"readonly_fields"`
	Ordering       []string                  `json:"ordering"`
	ModelFields    []*modelFieldSpec         `json:"model_fields"`
	Prefix         string                    `json:"prefix"`
	Autocomplete   []string                  `json:"autocomplete_fields"`
}

type relatedObjectSpec struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Field       string `json:"field"`
	Column      string `json:"column"`
	Table       string `json:"table"`
	OnDelete    string `json:"on_delete"`
	ManyToMany  bool   `json:"many_to_many"`
	VerboseName string `json:"verbose_name"`
	PK          string `json:"pk"`
}

type uniqueConstraintSpec struct {
	Fields    []string `json:"fields"`
	Condition *string  `json:"condition"`
	Name      string   `json:"name"`
}

type modelSpec struct {
	Label             string                    `json:"label"`
	AppLabel          string                    `json:"app_label"`
	ModelName         string                    `json:"model_name"`
	VerboseName       string                    `json:"verbose_name"`
	VerboseNamePlural string                    `json:"verbose_name_plural"`
	Table             string                    `json:"table"`
	PK                string                    `json:"pk"`
	AdminClass        string                    `json:"admin_class"`
	Ordering          []string                  `json:"ordering"`
	AdminOrdering     []string                  `json:"admin_ordering"`
	ListDisplay       []*listDisplaySpec        `json:"list_display"`
	ListDisplayLinks  []string                  `json:"list_display_links"`
	ListFilterNames   []string                  `json:"list_filter_names"`
	SearchFields      []string                  `json:"search_fields"`
	ListPerPage       int                       `json:"list_per_page"`
	ListMaxShowAll    int                       `json:"list_max_show_all"`
	DateHierarchy     *string                   `json:"date_hierarchy"`
	Actions           []string                  `json:"actions"`
	SaveOnTop         bool                      `json:"save_on_top"`
	SaveAs            bool                      `json:"save_as"`
	ReadonlyFields    []string                  `json:"readonly_fields"`
	FieldsetsAdd      json.RawMessage           `json:"fieldsets_add"`
	FieldsetsChange   json.RawMessage           `json:"fieldsets_change"`
	AddFormFields     map[string]*formFieldSpec `json:"add_form_fields"`
	ChangeFormFields  map[string]*formFieldSpec `json:"change_form_fields"`
	ModelFields       []*modelFieldSpec         `json:"model_fields"`
	M2MFields         []*modelFieldSpec         `json:"m2m_fields"`
	Inlines           []*inlineSpec             `json:"inlines"`
	FilterHorizontal  []string                  `json:"filter_horizontal"`
	Autocomplete      []string                  `json:"autocomplete_fields"`
	UniqueTogether    [][]string                `json:"unique_together"`
	UniqueConstraints []uniqueConstraintSpec    `json:"unique_constraints"`
	RelatedObjects    []*relatedObjectSpec      `json:"related_objects"`

	fieldsetsAdd    []adminFieldset
	fieldsetsChange []adminFieldset
	byName          map[string]*modelFieldSpec
}

// adminFieldset is one fieldset: a title, and rows of one or more field names.
type adminFieldset struct {
	Title       *string
	Classes     []string
	Description string
	Rows        [][]string
}

func parseFieldsets(raw json.RawMessage) []adminFieldset {
	var items [][]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		panic(fmt.Errorf("admin spec: fieldsets: %w", err))
	}
	var out []adminFieldset
	for _, it := range items {
		var fs adminFieldset
		var title *string
		_ = json.Unmarshal(it[0], &title)
		fs.Title = title
		var opts struct {
			Fields      []json.RawMessage `json:"fields"`
			Classes     []string          `json:"classes"`
			Description string            `json:"description"`
		}
		_ = json.Unmarshal(it[1], &opts)
		fs.Classes, fs.Description = opts.Classes, opts.Description
		for _, f := range opts.Fields {
			var one string
			if json.Unmarshal(f, &one) == nil {
				fs.Rows = append(fs.Rows, []string{one})
				continue
			}
			var many []string
			_ = json.Unmarshal(f, &many)
			fs.Rows = append(fs.Rows, many)
		}
		out = append(out, fs)
	}
	return out
}

func (m *modelSpec) field(name string) *modelFieldSpec { return m.byName[name] }

// adminSite is the parsed spec.
type adminSite struct {
	spec    *adminSpecFile
	models  map[string]*modelSpec // by label
	order   []*modelSpec
	any     map[string]*adminAnyModel
	appList []string // app labels with registered models, sorted by verbose name
}

func loadAdminSite() *adminSite {
	var spec adminSpecFile
	if err := json.Unmarshal(adminSpecJSON, &spec); err != nil {
		panic(fmt.Errorf("admin spec: %w", err))
	}
	s := &adminSite{spec: &spec, models: map[string]*modelSpec{}, any: map[string]*adminAnyModel{}}
	for _, m := range spec.AllModels {
		s.any[m.Label] = m
	}
	for _, m := range spec.Models {
		m.fieldsetsAdd = parseFieldsets(m.FieldsetsAdd)
		m.fieldsetsChange = parseFieldsets(m.FieldsetsChange)
		m.byName = map[string]*modelFieldSpec{}
		for _, f := range m.ModelFields {
			m.byName[f.Name] = f
		}
		for _, f := range m.M2MFields {
			m.byName[f.Name] = f
		}
		s.models[m.Label] = m
		s.order = append(s.order, m)
	}
	return s
}

func (m *modelSpec) title() string { return capfirst(m.VerboseName) }

func capfirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}
