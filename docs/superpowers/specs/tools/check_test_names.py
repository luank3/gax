"""Check that every Test name a spec cites exists in the repository's test files."""

import pathlib
import re
import sys

root = pathlib.Path(__file__).resolve().parents[4]
names = set()
for path in root.rglob("*_test.go"):
    names.update(re.findall(r"^func (Test\w+)\(", path.read_text(errors="replace"), re.M))

problems = 0
for arg in sys.argv[1:]:
    spec = pathlib.Path(arg)
    cited = set(re.findall(r"`(Test\w+)`", spec.read_text())) | set(
        re.findall(r"\b(Test[A-Z]\w+)\b", spec.read_text())
    )
    missing = sorted(cited - names)
    print(f"{spec.name}: {len(cited)} test names cited, {len(missing)} unknown")
    for name in missing:
        print("   -", name)
    problems += len(missing)

sys.exit(1 if problems else 0)
