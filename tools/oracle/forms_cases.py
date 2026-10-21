"""Renders the deed wizard's Django forms/formsets for a set of scenarios and
records the normalised result, for internal/deedforms' golden test.

    forms_cases.py OUT.json        (needs the dev database; see /tmp/dj_shell.sh)
"""
import json
import re
import sys
from html.parser import HTMLParser

import django

django.setup()

from django.contrib.auth import get_user_model
from django.db import transaction
from django.http import QueryDict
from django.utils import timezone

from deeds import forms as F
from deeds.models import Claimant, Deed, Executant, PortalDropdown, Presenter, PriorDocument, RegistrationDetail, ScheduleItem, Witness
from deeds.wizard import DeedWizard

out_path = sys.argv[1]
today = timezone.localdate()


class P(HTMLParser):
    def __init__(self):
        super().__init__()
        self.el = None
        self.options = []
        self.text = ""
        self._opt = None
        self._in_ta = False

    def handle_starttag(self, tag, attrs):
        a = {k: (True if v is None or v == "" or v == k else v) for k, v in attrs}
        if tag in ("input", "select", "textarea") and self.el is None:
            self.el = {"tag": tag, "attrs": a}
        elif tag == "option":
            self._opt = {"value": dict(attrs).get("value"), "selected": "selected" in a, "text": ""}
        if tag == "textarea":
            self._in_ta = True

    def handle_endtag(self, tag):
        if tag == "option" and self._opt is not None:
            self.options.append(self._opt)
            self._opt = None
        if tag == "textarea":
            self._in_ta = False

    def handle_data(self, data):
        if self._opt is not None:
            self._opt["text"] += data
        elif self._in_ta:
            self.text += data


def parse_html(s):
    p = P()
    p.feed(s)
    el = dict(p.el or {})
    if p.options:
        el["options"] = [{"value": o["value"], "selected": o["selected"], "text": o["text"].strip()} for o in p.options]
    if el.get("tag") == "textarea":
        el["text"] = p.text.lstrip("\n")
    return el


def norm_form(form):
    fields = []
    for bf in form:
        fields.append({
            "name": bf.name, "label": str(bf.label), "required": bool(bf.field.required), "help_text": str(bf.help_text),
            "errors": [str(e) for e in bf.errors] if form.is_bound else [],
            "hidden": bf.is_hidden, "html": parse_html(str(bf)),
        })
    return {
        "fields": fields,
        "non_field_errors": [str(e) for e in form.non_field_errors()] if form.is_bound else [],
        "valid": form.is_valid() if form.is_bound else None,
    }


def norm_formset(fs):
    res = {
        "management": re.findall(r'name="([^"]+)" value="([^"]*)"', str(fs.management_form)),
        "non_form_errors": [str(e) for e in fs.non_form_errors()] if fs.is_bound else [],
        "forms": [norm_form(f) for f in fs.forms],
        "order": [f.prefix for f in fs.forms_in_page_order],
        "empty": norm_form(fs.empty_form),
        "valid": fs.is_valid() if fs.is_bound else None,
    }
    if fs.is_bound:
        res["deleted"] = [f.prefix for f in fs.deleted_forms]
        res["changed"] = [f.has_changed() for f in fs.forms]
    return res


User = get_user_model()
cases = []
setups = {}  # name -> {"dropdowns": bool}


def subst(obj, ids):
    if isinstance(obj, dict):
        return {k: subst(v, ids) for k, v in obj.items()}
    if isinstance(obj, list):
        return [subst(v, ids) for v in obj]
    if isinstance(obj, str):
        for k, v in ids.items():
            obj = obj.replace(k, str(v))
        return obj
    return obj


def unsubst(obj, ids):
    """Swaps real row ids for tokens, only in the hidden id / deed inputs."""
    inv = {str(v): k for k, v in ids.items()}
    if isinstance(obj, dict):
        if obj.get("name") in ("id", "deed") and isinstance(obj.get("html"), dict):
            attrs = obj["html"].get("attrs", {})
            if isinstance(attrs.get("value"), str) and attrs["value"] in inv:
                attrs["value"] = inv[attrs["value"]]
        return {k: unsubst(v, ids) for k, v in obj.items()}
    if isinstance(obj, list):
        return [unsubst(v, ids) for v in obj]
    return obj


MODELS = {"documents": PriorDocument, "executants": Executant, "claimants": Claimant, "schedule": ScheduleItem, "witnesses": Witness}
SET = {"documents": F.PriorDocumentFormSet, "executants": F.ExecutantFormSet, "claimants": F.ClaimantFormSet,
       "schedule": F.ScheduleItemFormSet, "witnesses": F.WitnessFormSet}
SINGLE = {"registration": (F.RegistrationDetailForm, RegistrationDetail), "presenter": (F.PresenterForm, Presenter)}

DEED_FIELDS = {"token_number": ""}

PARTY = dict(capacity="Executant", name="Anil Kumar", gender="Male", age=40, relation="മകന്‍", relative_name="Raman",
             job="Farmer", district="പാലക്കാട്", taluk="പട്ടാമ്പി", village="കൊപ്പം", house_name="H", post="P", place="Pl",
             pin="679307", mobile="9999999999", id_type="", id_number="123")
SCHED = dict(number="1", district="പാലക്കാട്", sro="വിളയൂർ", taluk="പട്ടാമ്പി", village="കൊപ്പം", desam="X", local_body_type="പഞ്ചായത്ത്",
             local_body_name="കൊപ്പം", resurvey_block="", resurvey_number="12", resurvey_sub="", old_survey="45", old_sub="1",
             land_type="Dry Land", tp_number="", total_hectare="0", total_are="10.5", total_sqm="0", transfer_hectare="0",
             transfer_are="10.5", transfer_sqm="0", price=125000, fair_value=10000, details="d", boundary_east="e",
             boundary_north="n", boundary_west="w", boundary_south="s")
PRIOR = dict(document_type="രജിസ്റ്റർ/ഫയൽ ചെയ്ത", district="പാലക്കാട്", sro="വിളയൂർ", number="870", year=1990, book=1, volume=275,
             page_from=87, page_to=93, remarks="")
WIT = dict(witness_type="attestation", portal_witness_type="WB", name="W One", gender="Male")
REG = dict(district="പാലക്കാട്", sro="വിളയൂർ", taluk="പട്ടാമ്പി", book=1, deed_type="വിലയാധാരം",
           deed_sub_type="വിലയാധാരം - (തീറാധാരം)--0702")
PRES = dict(name="Anil Kumar", presenter_type="Executant", gender="Male", age=40, presenter_date=str(today), stamp_date=str(today),
            document_sheet_count=5, document_copies=0, document_value=125000, mobile="9999999999")


def run(name, section, rows=None, post=None, extra_post=None, dropdowns=False, token="", min_value=None, doc_value=None,
        wizard_defaults=False, locked=False):
    """One scenario. rows: instance fields for the section; post: raw POST dict (None = unbound)."""
    cases.append(dict(name=name, section=section, rows=rows or [], post=post, dropdowns=dropdowns, token=token,
                      min_value=min_value, doc_value=doc_value, wizard_defaults=wizard_defaults, locked=locked))


DROPDOWN_FIXTURE = [
    ("registration", "ddDistrict", [], [["1", "പാലക്കാട്"], ["2", "തൃശ്ശൂർ"]]),
    ("registration", "ddSro", [["ddDistrict", "1", "പാലക്കാട്"]], [["10", "വിളയൂർ"], ["11", "പട്ടാമ്പി"]]),
    ("registration", "ddTaluk", [["ddDistrict", "1", "പാലക്കാട്"], ["ddSro", "10", "വിളയൂർ"]], [["100", "പട്ടാമ്പി"]]),
    ("registration", "selddMainTtype", [["selddbookno", "1", "1 - Immovable Property"]], [["a", "വിലയാധാരം"], ["b", "ദാനം"]]),
    ("registration", "selddTtype", [["selddbookno", "1", "1 - Immovable Property"], ["selddMainTtype", "a", "വിലയാധാരം"]], [["x", "വിലയാധാരം - (തീറാധാരം)--0702"]]),
    ("prior_document", "documentType", [], [["R", "രജിസ്റ്റർ/ഫയൽ ചെയ്ത (Registered/Filed)"], ["W", "വിൽ"]]),
    ("prior_document", "district", [["documentType", "R", "രജിസ്റ്റർ/ഫയൽ ചെയ്ത (Registered/Filed)"]], [["1", "പാലക്കാട്"]]),
    ("prior_document", "sro", [["documentType", "R", "രജിസ്റ്റർ/ഫയൽ ചെയ്ത (Registered/Filed)"], ["district", "1", "പാലക്കാട്"]], [["10", "വിളയൂർ"]]),
    ("executant", "district", [], [["1", "പാലക്കാട്"]]),
    ("executant", "thaluk", [["district", "1", "പാലക്കാട്"]], [["100", "പട്ടാമ്പി"]]),
    ("executant", "village", [["district", "1", "പാലക്കാട്"], ["thaluk", "100", "പട്ടാമ്പി"]], [["1000", "കൊപ്പം"]]),
    ("property", "district", [], [["1", "പാലക്കാട്"]]),
    ("property", "landType", [], [["1", "Dry Land"], ["2", "Wet(land)"]]),
]

# ---- scenarios -----------------------------------------------------------------------------
SECOND = {"documents": lambda b: dict(b, number="2"), "executants": lambda b: dict(b, name="Second"),
          "claimants": lambda b: dict(b, name="Second"), "schedule": lambda b: dict(b, number="2"),
          "witnesses": lambda b: dict(b, name="Second")}
for dd in (False, True):
    sfx = "+dd" if dd else ""
    run("reg-new" + sfx, "registration", dropdowns=dd)
    run("reg-saved" + sfx, "registration", rows=[REG], dropdowns=dd)
    run("reg-mapped" + sfx, "registration", rows=[dict(REG, deed_type="തീരാധാരം", deed_sub_type="")], dropdowns=dd)
    run("reg-locked" + sfx, "registration", rows=[REG], dropdowns=dd, locked=True)
    run("reg-post-ok" + sfx, "registration", rows=[REG], post={"registration-district": "പാലക്കാട്", "registration-sro": "വിളയൂർ",
        "registration-taluk": "പട്ടാമ്പി", "registration-book": "1", "registration-deed_type": "വിലയാധാരം",
        "registration-deed_sub_type": "വിലയാധാരം - (തീറാധാരം)--0702"}, dropdowns=dd)
    run("reg-post-bad" + sfx, "registration", post={"registration-district": "Nowhere", "registration-sro": "x", "registration-book": "9"}, dropdowns=dd)
    run("reg-post-empty" + sfx, "registration", post={}, dropdowns=dd)
    run("pres-new" + sfx, "presenter", dropdowns=dd, wizard_defaults=True, min_value=250000)
    run("pres-saved" + sfx, "presenter", rows=[PRES], dropdowns=dd, wizard_defaults=True)
    run("pres-past", "presenter", rows=[dict(PRES, presenter_date="2020-01-02", stamp_date=None, document_sheet_count=None)], wizard_defaults=True)
    run("pres-post-ok", "presenter", post={"presenter-presenter_type": "Executant", "presenter-name": "A", "presenter-presenter_date": str(today),
        "presenter-document_value": "300000", "presenter-document_copies": "0"}, min_value=250000)
    run("pres-post-bad", "presenter", post={"presenter-presenter_type": "Bogus", "presenter-presenter_date": "2020-01-01",
        "presenter-stamp_date": "2999-01-01", "presenter-age": "99999", "presenter-document_value": "10", "presenter-pin": "1234567",
        "presenter-document_copies": ""}, min_value=250000)
    for sec, base in (("documents", PRIOR), ("executants", PARTY), ("claimants", dict(PARTY, capacity="Claimant")),
                      ("schedule", SCHED), ("witnesses", WIT)):
        run(f"{sec}-none{sfx}", sec, dropdowns=dd)
        run(f"{sec}-two{sfx}", sec, rows=[base, SECOND[sec](base)], dropdowns=dd)

run("sched-doc-value", "schedule", rows=[SCHED], doc_value=125000)
base_post = lambda sec, n, init, rows: {f"{sec}-TOTAL_FORMS": str(n), f"{sec}-INITIAL_FORMS": str(init), f"{sec}-MIN_NUM_FORMS": "0", f"{sec}-MAX_NUM_FORMS": "1000", **rows}

def row_post(sec, i, ident, fields, delete=False):
    d = {f"{sec}-{i}-{k}": ("" if v is None else str(v)) for k, v in fields.items()}
    d[f"{sec}-{i}-id"] = ident
    d[f"{sec}-{i}-deed"] = "{DEED}"
    if delete:
        d[f"{sec}-{i}-DELETE"] = "on"
    return d

for sec, base in (("executants", PARTY), ("documents", PRIOR), ("witnesses", WIT), ("claimants", dict(PARTY, capacity="Claimant"))):
    # valid post: one saved row unchanged + one new row filled
    p = base_post(sec, 3, 1, {})
    p.update(row_post(sec, 0, "{ROW0}", base))
    p.update(row_post(sec, 1, "", dict(base, name="Newbie")))
    p.update(row_post(sec, 2, "", {k: "" for k in base} if sec != "witnesses" else {**{k: "" for k in base}, "portal_witness_type": "WB"}))
    p[f"{sec}-ROWS"] = "1,0,2"
    run(f"{sec}-post-ok", sec, rows=[base], post=p)
    # delete saved, blank new
    p = base_post(sec, 2, 1, {})
    p.update(row_post(sec, 0, "{ROW0}", base, delete=True))
    p.update(row_post(sec, 1, "", {k: "" for k in base} if sec != "witnesses" else {**{k: "" for k in base}, "portal_witness_type": "WB"}))
    run(f"{sec}-post-delete", sec, rows=[base], post=p)
    # no management form
    run(f"{sec}-post-nomgmt", sec, rows=[base], post={})
    # bad id
    p = base_post(sec, 1, 1, {})
    p.update(row_post(sec, 0, "999999999", base))
    run(f"{sec}-post-badid", sec, rows=[base], post=p)
    # invalid row
    p = base_post(sec, 1, 0, {})
    p.update(row_post(sec, 0, "", {**base, "name": ""}))
    run(f"{sec}-post-invalid", sec, post=p)
    # nothing at all (min_num)
    p = base_post(sec, 1, 0, {})
    p.update(row_post(sec, 0, "", {k: "" for k in base} if sec != "witnesses" else {**{k: "" for k in base}, "portal_witness_type": "WB"}))
    run(f"{sec}-post-empty", sec, post=p)

# schedule: price sum
p = base_post("schedule", 2, 1, {})
p.update(row_post("schedule", 0, "{ROW0}", SCHED))
p.update(row_post("schedule", 1, "", dict(SCHED, number="2", price=500)))
run("schedule-post-sum", "schedule", rows=[SCHED], post=p, doc_value=999999)
run("schedule-post-sum-ok", "schedule", rows=[SCHED], post=p, doc_value=125500)
p = base_post("schedule", 1, 1, {})
p.update(row_post("schedule", 0, "{ROW0}", dict(SCHED, price=5, fair_value=10000)))
run("schedule-post-minprice", "schedule", rows=[SCHED], post=p)

# ---- run -------------------------------------------------------------------------------------
user, _ = User.objects.get_or_create(username="oracle_forms_user")
results = []
with transaction.atomic():
    for case in cases:
        sid = transaction.savepoint()
        try:
            PortalDropdown.objects.all().delete()
            if case["dropdowns"]:
                from datetime import datetime, timezone as tz
                from deeds.utils.portal_dropdowns import make_parent_key
                now = datetime.now(tz.utc)
                for page, field, parents, options in DROPDOWN_FIXTURE:
                    PortalDropdown.objects.create(page=page, field=field, label=field, parents=parents,
                                                  parent_key=make_parent_key(parents), options=options, scraped_at=now)
            deed = Deed.objects.create(owner=user, token_number=case["token"])
            ids = {"{DEED}": deed.pk}
            sec = case["section"]
            inst_rows = []
            if sec in SINGLE:
                form_cls, model = SINGLE[sec]
                instance = None
                if case["rows"]:
                    fields = dict(case["rows"][0])
                    for k in ("presenter_date", "stamp_date"):
                        if k in fields and fields[k]:
                            fields[k] = timezone.datetime.fromisoformat(str(fields[k])).date()
                    instance = model.objects.create(deed=deed, **fields)
                data = QueryDict(mutable=True) if case["post"] is not None else None
                if data is not None:
                    for k, v in case["post"].items():
                        data[k] = v
                form = form_cls(data=data, instance=instance, prefix=sec) if data is not None else form_cls(instance=instance, prefix=sec)
                if sec == "registration" and case["locked"]:
                    for f in form.fields.values():
                        f.disabled = True
                        f.required = False
                if sec == "presenter":
                    form.set_minimum_document_value(case["min_value"])
                    if data is None and case["wizard_defaults"]:
                        DeedWizard._presenter_defaults(form)
                res = norm_form(form)
            else:
                model = MODELS[sec]
                created = []
                for r in case["rows"]:
                    r = dict(r)
                    created.append(model.objects.create(deed=deed, **r))
                for i, o in enumerate(created):
                    ids[f"{{ROW{i}}}"] = o.pk
                data = None
                if case["post"] is not None:
                    data = QueryDict(mutable=True)
                    for k, v in subst(case["post"], ids).items():
                        data[k] = v
                fs = SET[sec](data=data, instance=deed, prefix=sec) if data is not None else SET[sec](instance=deed, prefix=sec)
                if sec == "schedule":
                    fs.document_value = case["doc_value"]
                res = norm_formset(fs)
            res = unsubst(res, ids)
            results.append(dict(case, expected=res))
        finally:
            transaction.savepoint_rollback(sid)
    transaction.set_rollback(True)
json.dump({"today": str(today), "dropdowns": DROPDOWN_FIXTURE, "cases": results}, open(out_path, "w", encoding="utf-8"), ensure_ascii=False, indent=1, default=str)
print(len(results), "cases")
