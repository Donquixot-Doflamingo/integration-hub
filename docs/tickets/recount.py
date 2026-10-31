#!/usr/bin/env python3
"""Recompute ticket counters in TICKETS.md and README.md from the tickets themselves.

Run after adding tickets or changing statuses (and after merges that touched TICKETS.md):
    python3 docs/tickets/recount.py          # rewrite counters
    python3 docs/tickets/recount.py --check  # exit 1 if counters are stale (CI-friendly)
"""
import collections
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
TICKETS, README = HERE / "TICKETS.md", HERE / "README.md"

text = TICKETS.read_text()
blocks = re.split(r"(?m)^### (?=HUB-\d+ )", text)[1:]
epic_total, epic_done = collections.Counter(), collections.Counter()
feat_total = collections.Counter()
status = collections.Counter()
for b in blocks:
    epic = re.search(r"\*\*Epic:\*\* (EP-\d+)", b).group(1)
    st = re.search(r"\*\*Status:\*\* ([\w-]+)", b).group(1)
    feats = re.search(r"\*\*Feature:\*\* ([^·\n]+)", b).group(1)
    epic_total[epic] += 1
    epic_done[epic] += st == "done"
    status[st] += 1
    for f in re.findall(r"F-\d+", feats):
        feat_total[f] += 1

total = sum(epic_total.values())
other = total - status["done"] - status["todo"]
summary = f"Total tickets: **{total}** · done: **{status['done']}** · todo: **{status['todo']}**"
if other:
    summary += f" · other (in-progress/in-review/blocked): **{other}**"


def fix_epic_rows(s: str) -> str:
    # Epic table rows end with "| <tickets> | <done> |".
    def row(m):
        e = m.group(1)
        return f"{m.group(0)[: m.start(2) - m.start(0)]}{epic_total[e]} | {epic_done[e]} |"
    s = re.sub(r"(?m)^\| (EP-\d+) \|.*?\| (\d+) \| \d+ \|$", row, s)
    s = re.sub(r"(?m)^\| \*\*Total\*\* \|(.*?)\| \*\*\d+\*\* \| \*\*\d+\*\* \|$",
               lambda m: f"| **Total** |{m.group(1)}| **{total}** | **{status['done']}** |", s)
    return s


def fix_feature_rows(s: str) -> str:
    return re.sub(r"(?m)^(\| (F-\d+) \|[^\n]*\| )(\d+)( \|)$",
                  lambda m: f"{m.group(1)}{feat_total[m.group(2)]}{m.group(4)}", s)


new_tickets = fix_epic_rows(re.sub(r"(?m)^Total tickets: .*$", summary, text, count=1))
readme = README.read_text()
new_readme = fix_feature_rows(fix_epic_rows(readme))

if "--check" in sys.argv:
    stale = [p.name for p, old, new in ((TICKETS, text, new_tickets), (README, readme, new_readme)) if old != new]
    if stale:
        print("stale ticket counters in: " + ", ".join(stale) + " (run python3 docs/tickets/recount.py)")
        sys.exit(1)
    print("ticket counters up to date")
    sys.exit(0)
TICKETS.write_text(new_tickets)
README.write_text(new_readme)
print(summary.replace("**", ""))
