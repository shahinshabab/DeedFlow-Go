"""Generates random schedule tables and the entries TableExtractor makes of
them, for internal/extract's golden test.  Usage: table_cases.py BACKEND OUT"""
import json
import os
import random
import sys

sys.path.insert(0, sys.argv[1])
os.environ.setdefault("DJANGO_SETTINGS_MODULE", "config.settings")
from deeds.utils.property_reference import load_property_data
from deeds.utils.table_extraction import TableExtractor

random.seed(11)
data = load_property_data()
districts = list(data)


def pick(d):
    ks = list(d)
    return random.choice(ks) if ks else ""


def typo(s):
    if not s or random.random() < 0.6:
        return s
    i = random.randrange(len(s))
    return s[:i] + s[i + 1:] if random.random() < 0.5 else s[:i] + random.choice("അആഇഉകഗ") + s[i:]


def make_table():
    dist = pick(data)
    sros = data.get(dist, {}).get("sros", {})
    sro = pick(sros)
    taluks = sros.get(sro, {}).get("thaluks", {})
    taluk = pick(taluks)
    villages = taluks.get(taluk, {}).get("villages", {})
    village = pick(villages)
    desams = villages.get(village, {}).get("desams", {})
    desam = pick(desams)
    lb = list(data.get(dist, {}).get("local_bodies", {}).get("പഞ്ചായത്ത്", {}).get("lbody_names", {}))
    lbody = random.choice(lb) if lb else ""
    surveys = ["3", "12", "45 46", "7,8", "1 B", "100 101", "5,6 7", "66 67", "0", "......", "", "2 A 3", "9"]
    subs = ["1", "2 4", "A", "1,2", "0", "", "3 5,6", "1 2 4"]
    res = ["12", "CÃ", "Nil", "NA", "......", "-", "0", "", "ഇല്ല", "40 41", "7,8"]
    ares = ["10.5 BÀ", "2 BÀ 3.25 BÀ", "", "12 BÀ", "0.21 BÀ 5 BÀ 1 BÀ", "......", "4.5BÀ"]
    rows = []
    for i in range(21):
        rows.append([None, None, None, None, None])
    def cell(i, j, v):
        rows[i][j] = v
    cell(0, 4, random.choice(["1", "2", None, "", "10"]))
    cell(1, 4, typo(dist))
    cell(2, 4, typo(sro))
    cell(3, 4, typo(taluk))
    cell(4, 4, typo(village) + random.choice(["", " / ഗ്രാമം", "/x"]))
    cell(5, 4, typo(desam))
    cell(6, 4, typo(lbody))
    cell(7, 4, random.choice(res))
    cell(8, 4, random.choice(res))
    cell(9, 4, random.choice(subs))
    cell(10, 4, random.choice(surveys))
    cell(11, 4, random.choice(subs))
    cell(12, 4, random.choice(ares))
    cell(13, 4, random.choice(["", None, "abc"]))
    cell(14, 4, random.choice(["12/2020", None, ""]))
    cell(15, 4, random.choice(["", "9"]))
    cell(16, 2, random.choice(["വീടും പറമ്പും മീറ്റർ അളവിൽ കിഴക്ക്", "നാല് പോയിൻറ് രണ്ട് അഞ്ച് ആർ ഭൂമി", "ഇരുപത്തി എട്ട് പോയിൻറ് ഒന്ന് ആർ", None, "plain details (x)"]))
    for n in range(17, 21):
        cell(n, 3, random.choice(["കിഴക്ക്\nറോഡ് (x), y/z", "", None, "  a   b "]))
    # a few structural defects
    r = random.random()
    if r < 0.06:
        rows = rows[: random.randint(8, 12)]
    elif r < 0.12:
        rows[random.randint(8, 12)] = rows[0][:3] if len(rows) > 12 else []
    elif r < 0.16:
        rows = rows[:16]
    elif r < 0.2:
        rows[4][4] = None
    return rows


tables = [make_table() for _ in range(400)]
ex = TableExtractor()
out = []
for t in tables:
    entries = ex.extract_schedule([t])
    out.append({"table": t, "entries": entries})
json.dump(out, open(sys.argv[2], "w", encoding="utf-8"), ensure_ascii=False, default=str)
print(len(out), sum(1 for o in out if o["entries"]))
