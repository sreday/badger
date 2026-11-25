#!/usr/bin/env python3
"""A deploy must never break a link that already went out in an email.

tools/deploy.sh copies this file to the server and runs it there, as root:

    linkcheck.py save      before the new build goes in: note what answers now
    linkcheck.py verify    after the restart: everything that answered before
                           must answer the same; exit 1 if not (deploy.sh then
                           puts the old build back)

It asks the running server on 127.0.0.1 for every private page it knows
(Snap's anonymized lists, read from the data folder) and for each tool's
front door. It prints counts only, never an address: deploy logs are public.
"""
import hashlib
import json
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8081"
DIR = "/opt/badger-scan"
STATE = DIR + "/linkcheck.json"
DOORS = ["/robots.txt", "/", "/snout", "/snap/", "/sniff/", "/snoop/", "/snitch/", "/snore/", "/snort/", "/snug/"]
OLD_LINK = 60  # the first lists' links: 60 characters, with no /snap in front


def status(path):
    """The answer after redirects: 200, 401 (a password page), 404, 410... 0 = no answer."""
    try:
        with urllib.request.urlopen(BASE + path, timeout=10) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code
    except Exception:
        return 0


def paths():
    out = list(DOORS)
    try:
        with open(DIR + "/data/snap/exports.json") as f:
            lists = json.load(f)
    except (OSError, ValueError):
        lists = []
    for l in lists if isinstance(lists, list) else []:
        s = l.get("secret") if isinstance(l, dict) else None
        if not s:
            continue
        out.append("/snap/" + s)
        if len(s) == OLD_LINK:
            out.append("/" + s)
    return out


def key(path):
    return hashlib.sha256(path.encode()).hexdigest()


def works(code):
    return code in (200, 401)


def save():
    now = {key(p): status(p) for p in paths()}
    with open(STATE, "w") as f:
        json.dump(now, f)
    print("link check: %d pages noted, %d answer" % (len(now), sum(works(c) for c in now.values())))


def verify():
    for _ in range(30):  # the new build needs a moment to start
        if status("/robots.txt") == 200:
            break
        time.sleep(1)
    else:
        print("link check: the new build does not answer")
        return 1
    try:
        with open(STATE) as f:
            before = json.load(f)
    except (OSError, ValueError):
        before = {}
    broken = private = 0
    for p in paths():
        was = before.get(key(p))
        if was is None or not works(was):
            continue
        if status(p) != was:
            broken += 1
            private += p not in DOORS
    if broken:
        print("link check: %d pages that answered before the deploy do not answer now (%d of them private links people may hold)" % (broken, private))
        return 1
    print("link check: every page that answered before the deploy still answers (%d checked)" % sum(works(c) for c in before.values()))
    return 0


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    if mode == "save":
        save()
    elif mode == "verify":
        sys.exit(verify())
    else:
        sys.exit("usage: linkcheck.py save|verify")
