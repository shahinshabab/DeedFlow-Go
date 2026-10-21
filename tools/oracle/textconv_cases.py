import json, random, sys, os
sys.path.insert(0, sys.argv[1])
from deeds.utils.text_conversion import TextConverter
tc = TextConverter()
random.seed(11)
singles = [k for k in tc.font_map if len(k) == 1] + [" ", " ", "-", "\n", ".", ",", "v", "s", "t", "m", "{", "s", "t", "m", "v", "0", "5"]
cases = []
real = ["sImÃw 2024 ]n-w", "`mK-\n]{- X§Ä", "hkXv p", "mc- w", "mW.v", "{Ia-\\-¼À", "hkvXp hnh-cWw", "sSIv\ntm"]
for t in real:
    cases.append({"in": t, "out": tc.convert_ascii_to_unicode(t)})
for _ in range(4000):
    n = random.randint(1, 30)
    t = "".join(random.choice(singles) for _ in range(n))
    cases.append({"in": t, "out": tc.convert_ascii_to_unicode(t)})
json.dump(cases, open(sys.argv[2], "w"), ensure_ascii=False)
print(len(cases))
