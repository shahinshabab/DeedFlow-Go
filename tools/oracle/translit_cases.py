import json, random, sys, os
sys.path.insert(0, sys.argv[1])
from indic_transliteration import sanscript
from indic_transliteration.sanscript import transliterate
data = json.load(open(os.path.join(sys.argv[1], "deeds", "data", "property_data.json"), encoding="utf-8")) if os.path.exists(os.path.join(sys.argv[1], "deeds", "data", "property_data.json")) else {}
names = set()
def walk(o):
    if isinstance(o, dict):
        for k, v in o.items():
            names.add(k); walk(v)
    elif isinstance(o, list):
        for v in o: walk(v)
walk(data)
names = sorted(n for n in names if any("ഀ" <= c <= "ൿ" for c in n))
random.seed(7)
chars = [chr(c) for c in range(0x0d02, 0x0d7f)] + [" ", "/", "1", "a", "-", "‍", "‌"]
cases = [{"In": n, "Out": transliterate(n, sanscript.MALAYALAM, sanscript.ITRANS)} for n in names[:2500]]
for _ in range(1500):
    s = "".join(random.choice(chars) for _ in range(random.randint(0, 14)))
    cases.append({"In": s, "Out": transliterate(s, sanscript.MALAYALAM, sanscript.ITRANS)})
json.dump(cases, open(sys.argv[2], "w", encoding="utf-8"), ensure_ascii=False)
print(len(cases))
