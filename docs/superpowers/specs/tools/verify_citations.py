import re, sys, pathlib
root = pathlib.Path(__file__).resolve().parents[4]
def check(path):
    text = path.read_text()
    cites = re.findall(r'`([\w./-]+\.go):(\d+)`', text)
    bad = []
    for f, ln in cites:
        p = root / f
        if not p.exists():
            bad.append(f"missing file {f}")
            continue
        n = len(p.read_text(errors="replace").splitlines())
        if int(ln) > n:
            bad.append(f"{f}:{ln} beyond EOF ({n})")
    # test names
    tests = re.findall(r'`(Test\w+)` in\s*\n?\s*`([\w./-]+_test\.go):(\d+)`', text)
    for name, f, ln in tests:
        p = root / f
        if not p.exists():
            bad.append(f"missing test file {f}")
            continue
        src = p.read_text(errors="replace")
        lines = src.splitlines()
        if f"func {name}(" not in src:
            bad.append(f"{f}: no func {name}")
        elif int(ln) <= len(lines) and f"func {name}(" not in lines[int(ln)-1]:
            bad.append(f"{f}:{ln} is not func {name}")
    return cites, tests, bad

for arg in sys.argv[1:]:
    p = pathlib.Path(arg)
    cites, tests, bad = check(p)
    print(f"{p.name}: {len(cites)} source cites, {len(tests)} test cites, {len(bad)} problems")
    for b in bad:
        print("   -", b)
