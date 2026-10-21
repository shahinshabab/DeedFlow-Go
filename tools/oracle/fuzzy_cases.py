"""Writes golden cases for internal/fuzzy from the Python libraries the Django
project used. Run with a venv that has fuzzywuzzy + python-Levenshtein."""
import json, random, difflib, sys
from fuzzywuzzy import fuzz

random.seed(7)
alphabet = "abcdeഅആഇഈക്കരമ പ്ല-ൻര്‍ "
def rnd(n):
    return "".join(random.choice(alphabet) for _ in range(n))

cases = []
pairs = [("abcd", "abxd"), ("residential plot", "xx residential plt yy"), ("", "a"), ("a", ""), ("same", "same"),
         ("വിളയൂർ", "വിളയൂര്‍"), ("hello world", "yellow whirled")]
for _ in range(1500):
    a = rnd(random.randint(0, 14))
    b = rnd(random.randint(0, 40)) if random.random() < .5 else a[:random.randint(0, len(a))] + rnd(random.randint(0, 6)) + a[random.randint(0, len(a)):]
    pairs.append((a, b))
for a, b in pairs:
    cases.append({
        "a": a, "b": b,
        "ratio": fuzz.ratio(a, b), "partial": fuzz.partial_ratio(a, b),
        "seq_ratio": difflib.SequenceMatcher(None, a, b).ratio(),
        "seq_quick": difflib.SequenceMatcher(None, a, b).quick_ratio(),
        "seq_blocks": [list(m) for m in difflib.SequenceMatcher(None, a, b).get_matching_blocks()],
    })
json.dump(cases, open(sys.argv[1], "w"), ensure_ascii=False)
print(len(cases))
