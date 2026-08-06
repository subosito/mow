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

# Model is pinned rather than inherited from the user's config: this test
# asserts on rendered geometry, so a run that fails for an unrelated reason
# (a wire that rejects a registered tool's schema, a model without tool
# support) reports as a rendering failure and wastes the trail. Override with
# SMOKE_MODEL when checking a different endpoint.
SMOKE_MODEL="${SMOKE_MODEL:-glm-5.2}"

echo "=== driving mowi ==="
echo "model: $SMOKE_MODEL"
# mowi logs warnings (e.g. wire capability notices) to stderr, which shares the
# PTY with the rendered UI and corrupts the per-cell reads below. Send it to a
# file so `cells` only ever sees what the renderer painted.
shell-use run --cols 100 --rows 30 -- \
  sh -c "exec ./bin/mowi --workspace '$WS' --model '$SMOKE_MODEL' 2>'$WS/mowi.err'" >/dev/null 2>&1
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

# The bug a string test cannot see: signs must share one column. Two layouts
# have broken this -- right-aligning the sign inside the number cells, and
# bracketing the number ("+11" vs "11-"). Both zigzag down a replace pair.
check("change glyphs share a column (%d vs %d)" % (info["del"]["sign"], info["add"]["sign"]),
      info["del"]["sign"] == info["add"]["sign"] and info["del"]["sign"] > 0)

# Band is a rectangle, not a stripe that stops at the text.
for kind in ("del", "add"):
    b = info[kind]["band"]
    check("%s row is a continuous band (cols %s)" % (kind, b), b is not None and b[1] - b[0] > 40)

check("add and del bands are different colours (%s vs %s)" % (info["del"]["bg"], info["add"]["bg"]),
      info["del"]["bg"] != info["add"]["bg"])

# Line numbers must stay legible once the row carries a wash. The muted+dim
# gutter tone measured 1.65:1 on the add band and 1.90:1 on the del band --
# invisible in exactly the surface you navigate by. Only a per-cell read can
# see this; the string is identical either way.
def luminance(hexstr):
    h = hexstr.lstrip("#")
    parts = [int(h[i:i+2], 16) / 255 for i in (0, 2, 4)]
    lin = [c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4 for c in parts]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]

def contrast(fg, bg):
    a, b = sorted((luminance(fg), luminance(bg)), reverse=True)
    return (a + 0.05) / (b + 0.05)

# Line numbers carry the change direction as a tint, not as a band. Banding
# them forced legible text onto a mid-tone background, which needed a chain of
# contrast machinery to keep readable; tinting says the same thing (green =
# inserted, red = removed) against the terminal's own background.
def gutter_cells(y):
    """Cells left of the separator: numbers and their glyphs live here."""
    cs = cells(0, y, 80)
    for i, c in enumerate(cs):
        if c["char"] == "\u2502":
            return cs[:i]
    return cs

for kind, y in rows.items():
    nums = [c for c in gutter_cells(y) if c["char"].isdigit()]
    if not nums:
        check("%s row: line numbers present in the gutter" % kind, False)
        continue
    banded = [c for c in nums if c["bg"] != "default"]
    check("%s row line numbers carry no band (%d/%d washed)" % (kind, len(banded), len(nums)),
          len(banded) == 0)
    # Tinted, not plain: the direction must survive a greyscale reading too,
    # but colour is the primary signal and it has to actually be applied.
    tinted = [c for c in nums if c["fg"].startswith("#")]
    check("%s row line numbers are tinted (%d/%d)" % (kind, len(tinted), len(nums)),
          len(tinted) == len(nums))

# Add and del numbers must not share a tint, or the gutter says "changed"
# without saying which way.
if len(rows) == 2:
    def num_fg(y):
        for c in gutter_cells(y):
            if c["char"].isdigit():
                return c["fg"]
        return None
    del_fg = num_fg(rows["del"])
    add_fg = num_fg(rows["add"])
    check("add and del line numbers differ in tint (%s vs %s)" % (del_fg, add_fg),
          del_fg is not None and add_fg is not None and del_fg != add_fg)

# An edit that replaces a whole block sends -old/+new for every line, including
# the ones whose text never changed. Those identical pairs must render as
# context, otherwise a two-token edit paints as a wall of band and the real
# change loses all emphasis.
#
# Whether the diff contains such a pair is up to the model: a minimal edit
# touches only the changed lines and there is nothing to assert. Skip rather
# than fail, so a well-behaved model does not report as a rendering bug.
unchanged = [i for i, line in enumerate(screen)
             if "func New() *Client {" in line and "\u2212" not in line and "+" not in line]
if not unchanged:
    print("SKIP  unchanged-line check (model sent a minimal diff; no identical pair)")
else:
    cs = cells(0, unchanged[0], 80)
    banded = [c for c in cs if c["bg"] != "default"]
    check("unchanged line carries no add/del band (%d washed cells)" % len(banded),
          len(banded) == 0)

sys.exit(0 if ok else 1)
PY
check "diff geometry" $?

echo
if [ "$fail" -eq 0 ]; then echo "smoke-tui: ok"; else echo "smoke-tui: FAILURES"; fi
exit "$fail"
