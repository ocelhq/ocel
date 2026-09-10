import re
import sys

SKIP = re.compile(
    r"(?:sym|un|self|backend|hyper)link|link(?:ed|ing|er)|linkedin|readlink|linkname",
    re.I,
)

WORD = re.compile(r"LINKS|LINK|Links|Link|links|link")

NEW = {
    "LINKS": "BINDINGS",
    "LINK": "BINDING",
    "Links": "Bindings",
    "Link": "Binding",
    "links": "bindings",
    "link": "binding",
}


def convert(text):
    edits = []
    for m in WORD.finditer(text):
        s, e = m.span()
        j = s
        while j > 0 and (text[j - 1].isalnum() or text[j - 1] == "_"):
            j -= 1
        k = e
        while k < len(text) and (text[k].isalnum() or text[k] == "_"):
            k += 1
        full = text[j:k]
        if SKIP.search(full):
            continue
        edits.append((s, e, NEW[m.group(0)]))
    out = []
    prev = 0
    for s, e, new in edits:
        out.append(text[prev:s])
        out.append(new)
        prev = e
    out.append(text[prev:])
    return "".join(out)


for path in sys.argv[1:]:
    t = open(path).read()
    n = convert(t)
    if n != t:
        open(path, "w").write(n)
        print("changed", path)
