#!/usr/bin/env bash
# TUI smoke test: drive the real mowi binary in a PTY and assert on what a
# terminal actually renders.
#
# The Go tests cover renderPrettyDiff's output string. They cannot see the
# painted grid, so they assert on ANSI substrings — which is how a sign-column
# misalignment survived (the "−" and "+" glyphs sat 6 columns apart while every
# unit test passed). shell-use reports per-cell char/fg/bg, so these checks are
# about geometry and colour as displayed.
#
# Not part of `just verify`: this needs a model endpoint and a network round
# trip. Run it by hand when touching mowi rendering.
set -uo pipefail

cd "$(dirname "$0")/.."

if ! command -v shell-use >/dev/null 2>&1; then
  echo "smoke-tui: shell-use not on PATH (enter the devenv shell, or the"
  echo "           platform has no pinned release — see devenv.nix)"
  exit 127
fi
if [ ! -x bin/mowi ]; then
  echo "smoke-tui: bin/mowi missing — run: just build-mowi"
  exit 1
fi

WS="$(mktemp -d)"
trap 'shell-use close >/dev/null 2>&1; shell-use kill >/dev/null 2>&1; rm -rf "$WS"' EXIT

cat > "$WS/cfg.go" <<'GO'
package cfg

func New() *Client {
	timeout := 30
	return newClient(timeout, false)
}
GO

fail=0
check() { # check <label> <condition-exit>
  if [ "$2" -eq 0 ]; then printf 'PASS  %s\n' "$1"; else printf 'FAIL  %s\n' "$1"; fail=1; fi
}

echo "=== driving mowi ==="
shell-use run --cols 100 --rows 30 -- ./bin/mowi --workspace "$WS" >/dev/null 2>&1
shell-use wait text "type a message" --timeout 30000 >/dev/null 2>&1
check "mowi painted its welcome frame" $?

shell-use type "in cfg.go change timeout 30 to 60 and false to true, edit the file" >/dev/null 2>&1
shell-use press Enter >/dev/null 2>&1

shell-use wait text "timeout := 60" --timeout 120000 >/dev/null 2>&1
check "edit applied and diff rendered" $?

echo
echo "=== diff geometry (per-cell) ==="
shell-use text > "$WS/screen.txt" 2>/dev/null
python3 - "$WS/screen.txt" <<'PY'
import json, subprocess, sys

screen = open(sys.argv[1]).read().split("\n")

def cells(x, y, w):
    out = subprocess.run(["shell-use", "cells", str(x), str(y), str(w), "1"],
                         capture_output=True, text=True).stdout
    return json.loads(out)["cells"]

rows = {}
for i, line in enumerate(screen):
    if "timeout :=" not in line:
        continue
    if "\u2212" in line:
        rows.setdefault("del", i)
    elif "+" in line:
        rows.setdefault("add", i)

if len(rows) != 2:
    print("FAIL  found %d changed rows, want a del/add pair" % len(rows))
    sys.exit(1)

info = {}
for kind, y in rows.items():
    cs = cells(0, y, 80)
    text = "".join(c["char"] for c in cs)
    band = [i for i, c in enumerate(cs) if c["bg"] != "default"]
    info[kind] = {
        "sign": text.find("\u2212" if kind == "del" else "+"),
        "band": (band[0], band[-1]) if band else None,
        "bg": cs[band[0]]["bg"] if band else None,
    }

ok = True
def check(label, cond):
    global ok
    print(("PASS  " if cond else "FAIL  ") + label)
    ok = ok and cond

# The bug a string test cannot see: signs must share one column.
check("change glyphs share a column (%d vs %d)" % (info["del"]["sign"], info["add"]["sign"]),
      info["del"]["sign"] == info["add"]["sign"] and info["del"]["sign"] > 0)

# Band is a rectangle, not a stripe that stops at the text.
for kind in ("del", "add"):
    b = info[kind]["band"]
    check("%s row is a continuous band (cols %s)" % (kind, b), b is not None and b[1] - b[0] > 40)

check("add and del bands are different colours (%s vs %s)" % (info["del"]["bg"], info["add"]["bg"]),
      info["del"]["bg"] != info["add"]["bg"])

sys.exit(0 if ok else 1)
PY
check "diff geometry" $?

echo
if [ "$fail" -eq 0 ]; then echo "smoke-tui: ok"; else echo "smoke-tui: FAILURES"; fi
exit "$fail"
