"""How a day of real traffic used the bus, read-only.

    python scripts/bus-usage.py [YYYY-MM-DD] [--home DIR]

The live hub.db is held by the hub process, so this copies hub.db and its
WAL to a temporary folder and reads the copy. Prints, for that day: sessions,
messages, task boards, and the queue waits per file (what a shared file in
the covenant is meant to remove).
"""
import argparse
import collections
import os
import re
import shutil
import sqlite3
import tempfile
from datetime import datetime, timedelta

SYSTEM = ("ainterlinked", "interlinked")


def snapshot(home):
    tmp = tempfile.mkdtemp(prefix="bus-usage-")
    for name in ("hub.db", "hub.db-wal"):
        src = os.path.join(home, name)
        if os.path.exists(src):
            shutil.copy2(src, os.path.join(tmp, name))
    return os.path.join(tmp, "hub.db"), tmp


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("day", nargs="?", default=datetime.now().strftime("%Y-%m-%d"))
    ap.add_argument("--home", default=os.environ.get("AINTERLINKED_HOME") or os.path.expanduser("~/.ainterlinked"))
    a = ap.parse_args()
    d0 = int(datetime.strptime(a.day, "%Y-%m-%d").timestamp() * 1000)
    d1 = d0 + int(timedelta(days=1).total_seconds() * 1000)
    path, tmp = snapshot(a.home)
    try:
        report(sqlite3.connect(path), a.day, d0, d1)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def report(c, day, d0, d1):
    q = lambda sql, *p: c.execute(sql, p).fetchall()
    joined = q("select agent_id, project from events where kind='joined' and ts between ? and ?", d0, d1)
    print(f"== {day}")
    print("sessions joined:", len({j[0] for j in joined}),
          dict(collections.Counter(p for _, p in {(i, p) for i, p in joined})))

    msgs = q("select sender_name, target, reply_to from messages where created_at between ? and ?", d0, d1)
    agents = [m for m in msgs if m[0] not in SYSTEM]
    print(f"messages: {len(msgs)} (agents {len(agents)}, hub notices {len(msgs) - len(agents)}); "
          f"replies {sum(1 for m in agents if m[2])}; senders {len({m[0] for m in agents})}")

    tasks = q("select board, status, created_by from tasks where created_at between ? and ?", d0, d1)
    print("tasks created per board:", dict(collections.Counter(t[0] for t in tasks)),
          "| status now:", dict(collections.Counter(t[1] for t in tasks)))
    moved = q("select summary from events where kind='task' and ts between ? and ?", d0, d1)
    print("task events:", len(moved))

    locks = [s for (s,) in q("select summary from events where kind='lock' and ts between ? and ?", d0, d1)]
    waits = collections.Counter()
    for s in locks:
        if "queued" in s.lower():
            m = re.search(r"queued for (\S+)", s)
            waits[m.group(1) if m else "?"] += 1
    print(f"lock events: {len(locks)}; queue waits: {sum(waits.values())}")
    for f, n in waits.most_common(10):
        print(f"  {n:4d}  {f}")


if __name__ == "__main__":
    main()
