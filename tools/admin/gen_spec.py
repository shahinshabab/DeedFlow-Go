"""Describes the Django admin of the DeedFlow project (its models, model
admins, forms, inlines, list filters ...) as JSON, for the Go admin to be
driven by.

    PYTHONPATH=<DeedFlow>/backend DJANGO_SETTINGS_MODULE=config.settings \
        python gen_spec.py > internal/web/admin_spec.json
"""

import json
import sys

import django

django.setup()

from django import forms  # noqa: E402
from django.contrib import admin  # noqa: E402
from django.contrib.admin import widgets as aw  # noqa: E402
from django.contrib.auth.models import User  # noqa: E402
from django.db import models  # noqa: E402
from django.test import RequestFactory  # noqa: E402


def jsonable(v):
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    if isinstance(v, (list, tuple)):
        return [jsonable(x) for x in v]
    if isinstance(v, dict):
        return {str(k): jsonable(x) for k, x in v.items()}
    if callable(v) and not isinstance(v, type):
        return None
    return str(v)


def model_field_spec(f):
    d = {
        "name": f.name, "column": getattr(f, "column", f.name), "attname": f.attname,
        "internal_type": f.get_internal_type(), "verbose_name": str(f.verbose_name),
        "help_text": str(f.help_text), "null": f.null, "blank": f.blank, "editable": f.editable,
        "primary_key": f.primary_key, "unique": f.unique, "max_length": f.max_length,
        "auto_now": getattr(f, "auto_now", False), "auto_now_add": getattr(f, "auto_now_add", False),
        "has_default": f.has_default(), "default": jsonable(f.default) if f.has_default() else None,
        "choices": [[str(k), str(v)] for k, v in f.flatchoices] if f.choices else None,
        "choice_values": [k for k, v in f.flatchoices] if f.choices else None,
    }
    if isinstance(f, models.DecimalField):
        d["max_digits"], d["decimal_places"] = f.max_digits, f.decimal_places
    if f.is_relation and f.related_model is not None:
        rm = f.related_model
        d["related"] = {"label": rm._meta.label_lower, "app_label": rm._meta.app_label,
                        "model_name": rm._meta.model_name, "verbose_name": str(rm._meta.verbose_name),
                        "table": rm._meta.db_table, "to_field": f.target_field.attname if hasattr(f, "target_field") else "id",
                        "one_to_one": f.one_to_one, "many_to_many": f.many_to_many,
                        "on_delete": getattr(getattr(f.remote_field, "on_delete", None), "__name__", None),
                        "ordering": list(rm._meta.ordering)}
    d["validators"] = [type(v).__name__ + ":" + str(getattr(v, "limit_value", "")) for v in f.validators]
    return d


def widget_spec(w):
    inner = getattr(w, "widget", None)
    spec = {"class": type(w).__name__, "attrs": jsonable(w.attrs), "input_type": getattr(w, "input_type", None)}
    if isinstance(w, admin.widgets.RelatedFieldWidgetWrapper):
        spec["inner"] = widget_spec(w.widget)
        spec["can_add_related"] = w.can_add_related
        spec["can_change_related"] = w.can_change_related
        spec["can_delete_related"] = w.can_delete_related
        spec["can_view_related"] = w.can_view_related
        spec["model_name"] = w.rel.model._meta.model_name
        spec["related_label"] = w.rel.model._meta.label_lower
    if hasattr(w, "choices") and not isinstance(w.choices, (forms.models.ModelChoiceIterator,)):
        try:
            spec["choices"] = [[str(k), str(v)] for k, v in w.choices]
        except Exception:
            pass
    if isinstance(w, admin.widgets.FilteredSelectMultiple):
        spec["verbose_name"] = str(w.verbose_name)
        spec["is_stacked"] = w.is_stacked
    return spec


def form_field_spec(name, f, model):
    d = {"name": name, "field_class": type(f).__name__, "label": str(f.label) if f.label is not None else None,
         "required": f.required, "help_text": str(f.help_text), "disabled": f.disabled,
         "widget": widget_spec(f.widget), "initial": jsonable(f.initial),
         "max_length": getattr(f, "max_length", None), "min_length": getattr(f, "min_length", None),
         "max_value": jsonable(getattr(f, "max_value", None)), "min_value": jsonable(getattr(f, "min_value", None)),
         "max_digits": getattr(f, "max_digits", None), "decimal_places": getattr(f, "decimal_places", None),
         "empty_value": jsonable(getattr(f, "empty_value", None)), "strip": getattr(f, "strip", None)}
    if isinstance(f, forms.ModelChoiceField):
        rm = f.queryset.model
        d["queryset"] = {"label": rm._meta.label_lower, "table": rm._meta.db_table, "ordering": list(f.queryset.query.order_by) or list(rm._meta.ordering),
                         "to_field": f.to_field_name, "empty_label": f.empty_label}
        d["multiple"] = isinstance(f, forms.ModelMultipleChoiceField)
    elif isinstance(f, forms.ChoiceField):
        d["choices"] = [[str(k), str(v)] for k, v in f.choices]
        if isinstance(f, forms.TypedChoiceField):
            d["coerce"] = getattr(f.coerce, "__name__", "str")
    try:
        mf = model._meta.get_field(name)
        d["model_field"] = model_field_spec(mf)
    except Exception:
        pass
    return d


def inline_spec(inl, req, parent_model):
    fs = inl.get_formset(req)
    form = fs.form
    fields = {n: form_field_spec(n, f, inl.model) for n, f in form.base_fields.items()}
    fk = fs.fk
    return {
        "class": type(inl).__name__, "template": inl.template, "model": inl.model._meta.label_lower,
        "model_name": inl.model._meta.model_name, "app_label": inl.model._meta.app_label,
        "verbose_name": str(inl.verbose_name), "verbose_name_plural": str(inl.verbose_name_plural),
        "table": inl.model._meta.db_table, "fk_name": fk.name, "fk_column": fk.column,
        "one_to_one": fk.one_to_one, "extra": inl.extra, "min_num": inl.min_num, "max_num": inl.max_num,
        "can_delete": inl.can_delete, "show_change_link": inl.show_change_link,
        "fields": list(fields), "form_fields": fields,
        "readonly_fields": list(inl.readonly_fields), "ordering": list(inl.model._meta.ordering),
        "model_fields": [model_field_spec(f) for f in inl.model._meta.concrete_fields],
        "prefix": fs.get_default_prefix(),
        "autocomplete_fields": list(getattr(inl, "autocomplete_fields", [])),
    }


def list_display_spec(ma, req):
    out = []
    for name in ma.get_list_display(req):
        item = {"name": name}
        if callable(name):
            item["kind"] = "callable"
        elif hasattr(ma, name) and callable(getattr(ma, name)):
            m = getattr(ma, name)
            item["kind"] = "admin_method"
            item["boolean"] = getattr(m, "boolean", False)
            item["short_description"] = str(getattr(m, "short_description", name.replace("_", " ")))
            item["admin_order_field"] = getattr(m, "admin_order_field", None)
        elif name == "__str__":
            item["kind"] = "str"
        else:
            try:
                f = ma.model._meta.get_field(name)
                item["kind"] = "field"
                item["field"] = model_field_spec(f)
            except Exception:
                item["kind"] = "attr"
        out.append(item)
    return out


def delete_graph(django_apps):
    """Every model with the relations that cascade a deletion, in the order
    Django's collector walks them."""
    from django.db.models.deletion import get_candidate_relations_to_delete

    out = []
    for m in django_apps.get_models(include_auto_created=True):
        opts = m._meta
        ordering = []
        for o in opts.ordering:
            desc = o.startswith("-")
            name = o.lstrip("-")
            try:
                col = opts.get_field(name).column
            except Exception:
                continue
            ordering.append({"column": col, "desc": desc})
        out.append({
            "label": opts.label_lower, "app_label": opts.app_label, "model_name": opts.model_name,
            "object_name": opts.object_name,
            "verbose_name": str(opts.verbose_name), "verbose_name_plural": str(opts.verbose_name_plural),
            "table": opts.db_table, "pk": opts.pk.name, "pk_column": opts.pk.column,
            "ordering": ordering, "auto_created": opts.auto_created is not False,
            "fields": [model_field_spec(f) for f in opts.concrete_fields],
            "relations": [{"model": r.related_model._meta.label_lower, "field": r.field.name,
                           "column": r.field.column, "on_delete": r.field.remote_field.on_delete.__name__,
                           "null": r.field.null}
                          for r in get_candidate_relations_to_delete(opts)],
        })
    return out


def main():
    req = RequestFactory().get("/")
    req.user = User.objects.filter(is_superuser=True).first()
    models_out = []
    apps = {}
    for model, ma in admin.site._registry.items():
        opts = model._meta
        apps[opts.app_label] = str(opts.app_config.verbose_name)
        add_form = ma.get_form(req)
        fieldsets_add = ma.get_fieldsets(req)
        # change form: use a real object when there is one
        obj = model.objects.first()
        change_form = ma.get_form(req, obj) if obj is not None else add_form
        fieldsets_change = ma.get_fieldsets(req, obj) if obj is not None else fieldsets_add
        spec = {
            "label": opts.label_lower, "app_label": opts.app_label, "model_name": opts.model_name,
            "verbose_name": str(opts.verbose_name), "verbose_name_plural": str(opts.verbose_name_plural),
            "table": opts.db_table, "pk": opts.pk.attname, "admin_class": type(ma).__name__,
            "ordering": list(opts.ordering), "admin_ordering": list(ma.get_ordering(req)),
            "list_display": list_display_spec(ma, req), "list_display_links": list(ma.get_list_display_links(req, ma.get_list_display(req))),
            "list_filter": [type(f).__name__ if not isinstance(f, (str, tuple)) else f for f in ma.get_list_filter(req)],
            "list_filter_names": [f if isinstance(f, str) else (f[0] if isinstance(f, tuple) else None) for f in ma.get_list_filter(req)],
            "search_fields": list(ma.get_search_fields(req)), "list_per_page": ma.list_per_page,
            "list_max_show_all": ma.list_max_show_all, "list_select_related": ma.list_select_related,
            "date_hierarchy": ma.date_hierarchy, "actions": list(ma.get_actions(req)),
            "save_on_top": ma.save_on_top, "save_as": ma.save_as,
            "readonly_fields": list(ma.get_readonly_fields(req)),
            "fieldsets_add": jsonable(fieldsets_add), "fieldsets_change": jsonable(fieldsets_change),
            "add_form_fields": {n: form_field_spec(n, f, model) for n, f in add_form.base_fields.items()},
            "change_form_fields": {n: form_field_spec(n, f, model) for n, f in change_form.base_fields.items()},
            "model_fields": [model_field_spec(f) for f in opts.concrete_fields],
            "m2m_fields": [model_field_spec(f) for f in opts.many_to_many],
            "inlines": [inline_spec(i, req, model) for i in ma.get_inline_instances(req)],
            "filter_horizontal": list(ma.filter_horizontal), "filter_vertical": list(ma.filter_vertical),
            "autocomplete_fields": list(ma.autocomplete_fields),
            "unique_together": jsonable(opts.unique_together),
            "unique_constraints": [{"fields": list(c.fields), "condition": str(c.condition) if c.condition else None, "name": c.name}
                                   for c in opts.constraints if isinstance(c, models.UniqueConstraint)],
            "related_objects": [{"name": r.get_accessor_name(), "label": r.related_model._meta.label_lower,
                                 "field": r.field.name, "column": r.field.column, "table": r.related_model._meta.db_table,
                                 "on_delete": getattr(r.on_delete, "__name__", None), "many_to_many": r.many_to_many, "verbose_name": str(r.related_model._meta.verbose_name),
                                 "pk": r.related_model._meta.pk.attname} for r in opts.related_objects],
        }
        models_out.append(spec)
    from django.apps import apps as django_apps

    all_models = [{"label": m._meta.label_lower, "app_label": m._meta.app_label, "model_name": m._meta.model_name,
                   "verbose_name": str(m._meta.verbose_name), "verbose_name_plural": str(m._meta.verbose_name_plural),
                   "app_verbose_name": str(m._meta.app_config.verbose_name), "table": m._meta.db_table}
                  for m in django_apps.get_models()]
    app_names = {c.label: str(c.verbose_name) for c in django_apps.get_app_configs()}
    json.dump({"apps": apps, "app_names": app_names, "all_models": all_models, "models": models_out,
               "delete_graph": delete_graph(django_apps)},
              sys.stdout, indent=1, sort_keys=False)


main()
