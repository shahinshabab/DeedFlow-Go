// Code generated from filereader/legacy.py; DO NOT EDIT.

package filereader

var legacyPreferred = map[string]string{
	"\u0d23\u0d4d\u0d1f": "\u00ad",
	"\u0d31\u0d4d\u0d31": "\u00e4",
	"\u0d1a\u0d4d\u0d1a": "\u00a8",
	"\u0d19\u0d4d\u0d15": "\u00a6",
	"\u0d2e\u0d4d\u0d2a": "\u00bc",
	"\u0d2e\u0d4d\u0d2e": "\u00bd",
	"\u0d2e\u0d4d\u0d32": "\u0178",
	"\u0d17\u0d4d\u0d17": "\u00a4",
	"\u0d28\u0d4d\u0d28": "\u00b6",
}

var legacyExtra = map[string]string{
	"\u0d08":             "Cu",
	"\u0d0a":             "Du",
	"\u0d10":             "sF",
	"\u0d13":             "Hm",
	"\u0d14":             "Hu",
	"\u0d12":             "H",
	"\u0d7c":             "\u00c0",
	"\u0d7d":             "\u00c2",
	"\u0d7e":             "\u00c4",
	"\u0d7b":             "\u00b3",
	"\u0d7a":             "\u00ac",
	"\u0d7b\u0d31":       "\u00e2",
	"\u0d28\u0d4d\u0d31": "\u00e2",
}

var legacyPreSigns = map[string]string{
	"\u0d46": "s",
	"\u0d47": "t",
	"\u0d48": "ss",
}

var legacySplitSigns = map[string][2]string{
	"\u0d4a": {"s", "m"},
	"\u0d4b": {"t", "m"},
	"\u0d4c": {"s", "u"},
}

var legacyPostSigns = map[string]string{
	"\u0d3e": "m",
	"\u0d3f": "n",
	"\u0d40": "o",
	"\u0d41": "p",
	"\u0d42": "q",
	"\u0d43": "r",
	"\u0d57": "u",
}

var legacyOther = map[string]string{
	"\u0d02": "w",
	"\u0d03": "x",
	"\u0d4d": "v",
}

const (
	legacyVirama  = "\u0d4d"
	legacyJoiners = "\u200c\u200d"
	legacyUnused  = "\u00e6\u00ea\u00e7\u00e5\u00ce\u00e3\u00d2\u00d0"
	legacySymbols = "\\{}[]|^`~"
)
