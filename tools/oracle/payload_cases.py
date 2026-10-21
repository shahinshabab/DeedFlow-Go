"""Golden cases for internal/deeds: runs the Django project's own payload
builder, fingerprints, row planner and preflight on random deeds (built from
plain objects, no database)."""
import datetime, json, os, random, sys, types
from decimal import Decimal

backend = sys.argv[1]
sys.path.insert(0, backend)
from django.conf import settings
settings.configure(USE_TZ=True, TIME_ZONE="Asia/Kolkata", INSTALLED_APPS=[])
import django
django.setup()
from django.utils import timezone

TODAY = datetime.date(2026, 10, 21)
timezone.localdate = lambda *a, **k: TODAY

from deeds.utils import portal_payload as pp
from deeds.utils import deed_rules

random.seed(int(sys.argv[3]) if len(sys.argv) > 3 else 5)
N = types.SimpleNamespace

class Mgr:
    def __init__(self, items): self.items = items
    def all(self): return list(self.items)

def pick(*xs): return random.choice(xs)
def maybe(x, p=.7): return x if random.random() < p else pick("", "......", None)

NAMES = ["സഫിയ", "മുസ്തഫ", "Anil Kumar", "റിട്ട. ഉദ്യോഗസ്ഥൻ", "", "ഷഹീറ", "A.B. Sam"]
IDS = ["631223992931", "6312 2399 2931", "TJO0571356", "123", "", "ABCDE1234F", "A1234567", "1234-5678-9012"]
PLACES = ["കിഴുമുറി", "വിളയൂർ", "", "Guntur, A.P.", "ചുണ്ടമ്പറ്റ"]
RELS = ["മകന്‍", "മകള്‍", "ഭാര്യ", "ബന്ധു", ""]
ID_TYPES = ["ആധാർ സവിശേഷ തിരിച്ചറിയൽ കാർഡ്", "ഡ്രൈവിംഗ് ലൈസന്‍സ്", "വോട്ടര്‍ തിരിച്ചറിയല്‍ കാര്‍ഡ്", "പാന്‍ കാര്‍ഡ്", "പാസ്സ്പോര്‍ട്ട്", "റേഷന്‍ കാര്‍ഡ്", ""]
DISTRICTS = ["പാലക്കാട്", "തിരുവനന്തപുരം", "Tamil Nadu", ""]

def party():
    return N(name=pick(*NAMES), capacity=pick("Executant", "Claimant", ""), house_name=maybe("കുറുപ്പുതൊടി"), age=pick(None, 18, 40, 65, 17),
             job=pick("സ്വന്തം", "", "റിട്ട. ഉദ്യോഗസ്ഥൻ", "farmer!"), relative_name=maybe("സുലൈമാൻ"), relation=pick(*RELS),
             gender=pick("Male", "Female", ""), place=pick(*PLACES), post=maybe("ആമയൂർ"), pin=pick("679303", "679 303", "", "560001", "12"),
             district=pick(*DISTRICTS), taluk=maybe("പട്ടാമ്പി"), village=maybe("കൊപ്പം"), id_type=pick(*ID_TYPES), id_number=pick(*IDS),
             mobile=pick("9999999999", "+91 98765 43210", "", "0123"))

def dec(): return pick(None, Decimal("0"), Decimal("1.52"), Decimal("0.0000"), Decimal("12.3456"), Decimal("250"), Decimal("1.5"))

def sched(n):
    return N(number=pick(str(n), "3/1", "", "2"), district="പാലക്കാട്", sro="വിളയൂർ", taluk="പട്ടാമ്പി", village="കൊപ്പം", desam=maybe("ആമയൂര്‍"),
             local_body_type="പഞ്ചായത്ത്", local_body_name="Koppam", resurvey_block=pick("024", "", "---"),
             resurvey_number=pick("282", "", "ഇല്ല", "Nil", "0", "100, 101", "15.....", "12/3"), resurvey_sub=pick("10/2", "", "1 B", "."),
             old_survey=pick("35", "", "66, 67", "1 2"), old_sub=pick("1", "", "1, 1B"), tp_number=pick("13420", ""),
             land_type=pick("Residential plot with Corp.Mun.Panch. road access", ""),
             total_hectare=dec(), total_are=dec(), total_sqm=dec(), transfer_hectare=dec(), transfer_are=dec(), transfer_sqm=dec(),
             price=pick(None, 0, 500000, 9999, 100), fair_value=pick(None, 50000, 1000, 0), pokkuvaravu=pick("Yes", "No", ""),
             undivided_share=pick("Yes", "No", ""), share=pick("", "1"), share_out_of=pick("", "2"),
             details=pick("തേക്കേ ഏളംകുളം പറമ്പ്", ""), boundary_east=pick("ഹംസ, സെനബ ഇവർ കൈവശം", "......", "", "10 അടി വീതി വഴി"),
             boundary_north=pick("10 അടി", "", "x/y"), boundary_west=pick("", "സെന്തിൽ"), boundary_south=pick("A.B (C)", ""))

def witness(kind):
    return N(witness_type=kind, portal_witness_type=pick("WB", "WE", "WP", "NW", ""), name=pick(*NAMES), gender=pick("Male", ""), relation=pick(*RELS),
             relative_name=maybe("പത്മനാഭൻ"), house_name=maybe("ചോലയിൽ"), job=pick("ടെക്നീഷ്യൻ", "a.b"), place=maybe("വിളയൂർ"), desam=maybe("വിളയൂർ"))

def prior():
    return N(document_type=pick("രജിസ്റ്റർ/ഫയൽ ചെയ്ത", "വിൽ", "പട്ടയം", "കോടതി ഉത്തരവ്", "രജിസ്റ്റർ ആധാരം", "Gift deed", ""),
             year=pick(None, 1980, 2014), book=pick(None, 1, 3), number=pick("1036", "LR6 83758/78", "", "70/2"), volume=pick(None, 879), page_from=pick(None, 353),
             page_to=pick(None, 355), district=pick("പാലക്കാട്", ""), sro=pick("വിളയൂർ", ""), remarks=pick("", "Register Bhagapathram (partition deed)", "a/b, c"))

def make_deed():
    n_sched = random.randint(0, 3)
    reg = N(district="പാലക്കാട്", sro="വിളയൂർ", taluk="പട്ടാമ്പി", book=pick(1, 3, 4), deed_type=pick("തീരാധാരം", "ദാനാധാരം", "വിലയാധാരം", "", "വിൽ", "ഒഴിമുറി"),
            deed_sub_type=pick("", "വിലയാധാരം - (തീറാധാരം)--0702", "ദാനം - കുടുംബാംഗത്തിന്‍ പേരിലുള്ളത് - Book-I--1209"))
    pres = N(presenter_type="Executant", presenter_date=pick(TODAY, TODAY - datetime.timedelta(days=3), None, TODAY + datetime.timedelta(days=2)),
             name=pick(*NAMES), gender=pick("Female", ""), age=pick(None, 58), relative_name=maybe("സുലൈമാൻ"), relation=pick(*RELS),
             job=pick("സ്വന്തം", "", "x.y"), house_name=maybe("കുറുപ്പുതൊടിയിൽ"), place=pick(*PLACES), post=maybe("ആമയൂർ"), pin=pick("679303", ""),
             mobile=pick("9999999999", ""), id_type=pick(*ID_TYPES), id_number=pick(*IDS), document_value=pick(None, 500000, 600000),
             stamp_date=pick(TODAY, None, TODAY + datetime.timedelta(days=1)), document_sheet_count=pick(None, 6), document_fees=pick(None, 600000, 100),
             stamp_mode=pick("Physical Paper", "E-Stamp", ""), document_copies=pick(0, 2, 25))
    deed = N(registration=reg, presenter=pres,
             prior_documents=Mgr([prior() for _ in range(random.randint(0, 3))]),
             executants=Mgr([party() for _ in range(random.randint(0, 3))]),
             claimants=Mgr([party() for _ in range(random.randint(0, 3))]),
             schedule_items=Mgr([sched(i + 1) for i in range(n_sched)]),
             witnesses=Mgr([witness("attesting") for _ in range(random.randint(0, 2))] + [witness("identifying") for _ in range(random.randint(0, 2))]),
             token_number=pick("", "T10380594"), status=pick("completed", "ready_for_review", "failed"),
             portal_snapshot={})
    return deed

def to_json(o):
    if isinstance(o, N): return {k: to_json(v) for k, v in vars(o).items()}
    if isinstance(o, Mgr): return [to_json(i) for i in o.items]
    if isinstance(o, (datetime.date,)): return o.isoformat()
    if isinstance(o, Decimal): return str(o)
    return o

cases = []
for _ in range(int(sys.argv[2])):
    deed = make_deed()
    try:
        payload = pp.build_portal_payload(deed)
    except Exception as exc:
        cases.append({"deed": to_json(deed), "error": True}); continue
    fps = pp.section_fingerprints(payload)
    rows = pp.row_fingerprints(payload)
    # a snapshot where some sections are confirmed and rows recorded, then some rows changed
    snap = {}
    for k in random.sample(list(fps), random.randint(0, len(fps))):
        snap[k] = fps[k] if random.random() < .6 else "stale"
    snap = pp.record_rows(snap, payload, [k for k in snap if k in pp.ROW_LISTS and random.random() < .8])
    # mutate recorded rows a little to exercise edit/add/remove paths
    for name, lst in (snap.get("_rows") or {}).items():
        r = random.random()
        if r < .3 and lst: lst[random.randrange(len(lst))]["fp"] = "old"
        elif r < .4 and lst: snap["_rows"][name] = lst[:-1]
        elif r < .5: snap["_rows"][name] = lst + [{"fp": "x", "key": "k"}]
    deed.portal_snapshot = snap
    sections = [k for k, _, _ in pp.PORTAL_SECTIONS if snap.get(k) != fps[k]]
    plan, whole = pp.plan_row_edits(deed, payload, sections)
    case = {
        "deed": to_json(deed), "payload": payload, "fingerprints": fps, "rows": rows,
        "problems_all": pp.preflight_problems(payload),
        "problems_some": pp.preflight_problems(payload, ["presenter", "schedule"]),
        "snapshot": snap, "sections": sections, "plan": plan, "whole": whole,
        "described": [pp.describe_row_change(n, c, payload) for n, cs in plan.items() for c in cs],
        "minimum_document_value": deed_rules.minimum_document_value(deed),
    }
    cases.append(case)
json.dump(cases, open(sys.argv[4] if len(sys.argv) > 4 else "/dev/stdout", "w"), ensure_ascii=False, default=str)
print(len(cases), file=sys.stderr)
