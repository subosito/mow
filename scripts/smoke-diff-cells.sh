#!/usr/bin/env bash
# Deterministic shell-use smoke for mowi diff band geometry and colours.
#
# Unlike smoke-tui.sh this does not need a model endpoint: it drives a Go test
# that paints a fixed unified diff via the real renderPrettyDiffPath path, then
# asserts on the painted cell grid (char/fg/bg) the same way smoke-tui does.
#
# Run: devenv shell -- just smoke-diff
set -uo pipefail

cd "$(dirname "$0")/.."

if ! command -v shell-use >/dev/null 2>&1; then
  echo "smoke-diff: shell-use not on PATH (enter the devenv shell, or the"
  echo "            platform has no pinned release — see devenv.nix)"
  exit 127
fi

WS="$(mktemp -d)"
BIN="$WS/mowi.test"
trap 'shell-use close >/dev/null 2>&1; shell-use kill >/dev/null 2>&1; rm -rf "$WS"' EXIT

fail=0
check() {
  if [ "$2" -eq 0 ]; then printf 'PASS  %s\n' "$1"; else printf 'FAIL  %s\n' "$1"; fail=1; fi
}

echo "=== build mowi test binary ==="
# Compile once so the PTY child is just the hanging paint test (no compile noise).
(
  cd packs/mowi
  go test -c -o "$BIN" .
) || { echo "smoke-diff: go test -c failed"; exit 1; }
check "compiled packs/mowi test binary" 0

echo
echo "=== paint fixed diff in PTY ==="
# COLORTERM/TERM force truecolor so lipgloss emits 48;2 bands shell-use can read.
shell-use run --cols 100 --rows 24 -- \
  env MOWI_DIFF_SMOKE=1 MOW_FORCE_COLOR=1 NO_COLOR= COLORTERM=truecolor TERM=xterm-256color \
  "$BIN" -test.run '^TestDiffSmokePaintForShellUse$' -test.v=false \
  >/dev/null 2>&1

shell-use wait text "SMOKE_DIFF_READY" --timeout 30000 >/dev/null 2>&1
check "diff paint fixture ready" $?

echo
echo "=== diff geometry + band colours (per-cell) ==="
shell-use text > "$WS/screen.txt" 2>/dev/null
python3 - "$WS/screen.txt" <<'PY'
import json, subprocess, sys

screen = open(sys.argv[1]).read().split("\n")

def cells(x, y, w):
    out = subprocess.run(
        ["shell-use", "cells", str(x), str(y), str(w), "1"],
        capture_output=True, text=True,
    ).stdout
    return json.loads(out)["cells"]

rows = {}
for i, line in enumerate(screen):
    if "timeout :=" not in line:
        continue
    if "\u2212" in line:
        rows.setdefault("del", i)
    elif "+" in line:
        rows.setdefault("add", i)

ok = True
def check(label, cond):
    global ok
    print(("PASS  " if cond else "FAIL  ") + label)
    ok = ok and cond

if len(rows) != 2:
    print("FAIL  found %d changed rows, want a del/add pair" % len(rows))
    print("screen:")
    for i, line in enumerate(screen[:20]):
        print("  %2d %r" % (i, line))
    sys.exit(1)

info = {}
for kind, y in rows.items():
    cs = cells(0, y, 90)
    text = "".join(c["char"] for c in cs)
    band = [i for i, c in enumerate(cs) if c.get("bg", "default") not in ("", "default")]
    info[kind] = {
        "y": y,
        "text": text.rstrip(),
        "sign": text.find("\u2212" if kind == "del" else "+"),
        "band": (band[0], band[-1]) if band else None,
        "bg": cs[band[0]]["bg"] if band else None,
        "cells": cs,
    }
    print("%s y=%d sign_col=%s band=%s bg=%s" % (
        kind, y, info[kind]["sign"], info[kind]["band"], info[kind]["bg"]))
    print("  text: %r" % info[kind]["text"][:80])

# Geometry: shared sign column (the historical 6-column bug).
check(
    "change glyphs share a column (%s vs %s)" % (info["del"]["sign"], info["add"]["sign"]),
    info["del"]["sign"] == info["add"]["sign"] and info["del"]["sign"] > 0,
)

# Band is a continuous rectangle, not a stripe that stops at the text.
for kind in ("del", "add"):
    b = info[kind]["band"]
    check(
        "%s row is a continuous band (cols %s)" % (kind, b),
        b is not None and b[1] - b[0] > 20,
    )

check(
    "add and del bands are different colours (%s vs %s)" % (info["del"]["bg"], info["add"]["bg"]),
    info["del"]["bg"] is not None
    and info["add"]["bg"] is not None
    and info["del"]["bg"] != info["add"]["bg"],
)

def luminance(hexstr):
    h = hexstr.lstrip("#")
    if len(h) != 6:
        return 0.0
    parts = [int(h[i:i+2], 16) / 255 for i in (0, 2, 4)]
    lin = [c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4 for c in parts]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]

def contrast(a, b):
    x, y = sorted((luminance(a), luminance(b)), reverse=True)
    return (x + 0.05) / (y + 0.05)

# Surface behind the panel: default dark userBg is #2A2A2E (pinned in the paint
# fixture). We measure against that so the band-floor assertion matches the
# unit tests rather than the terminal's unknown default background.
SURFACE = "#2a2b3c"
MIN_BAND = 1.18

for kind in ("del", "add"):
    bg = info[kind]["bg"]
    if bg is None or not str(bg).startswith("#"):
        check("%s band has hex bg (got %r)" % (kind, bg), False)
        continue
    ratio = contrast(bg, SURFACE)
    check(
        "%s band contrast %.2f >= %.2f (%s on %s)" % (kind, ratio, MIN_BAND, bg, SURFACE),
        ratio >= MIN_BAND,
    )

# Gutter: line numbers unwashed, tinted, signs present.
def gutter_cells(y):
    cs = cells(0, y, 90)
    for i, c in enumerate(cs):
        if c["char"] in ("\u2502", "|"):
            return cs[:i]
    # No box separator in compact panel: numbers + sign before first band cell.
    band_start = next((i for i, c in enumerate(cs) if c.get("bg", "default") not in ("", "default")), len(cs))
    return cs[:band_start]

for kind, y in rows.items():
    g = gutter_cells(y)
    nums = [c for c in g if c["char"].isdigit()]
    if not nums:
        check("%s row: line numbers present in the gutter" % kind, False)
        continue
    banded = [c for c in nums if c.get("bg", "default") not in ("", "default")]
    check(
        "%s row line numbers carry no band (%d/%d washed)" % (kind, len(banded), len(nums)),
        len(banded) == 0,
    )
    tinted = [c for c in nums if str(c.get("fg", "")).startswith("#")]
    check(
        "%s row line numbers are tinted (%d/%d)" % (kind, len(tinted), len(nums)),
        len(tinted) == len(nums),
    )

if len(rows) == 2:
    def num_fg(y):
        for c in gutter_cells(y):
            if c["char"].isdigit():
                return c.get("fg")
        return None
    del_fg = num_fg(rows["del"])
    add_fg = num_fg(rows["add"])
    check(
        "add and del line numbers differ in tint (%s vs %s)" % (del_fg, add_fg),
        del_fg is not None and add_fg is not None and del_fg != add_fg,
    )

# Context line (func New) must not carry an add/del wash.
ctx = [i for i, line in enumerate(screen)
       if "func New() *Client" in line and "\u2212" not in line and "+" not in line]
if not ctx:
    print("SKIP  context-line check (fixture row not found)")
else:
    cs = cells(0, ctx[0], 90)
    banded = [c for c in cs if c.get("bg", "default") not in ("", "default")]
    check(
        "context line carries no add/del band (%d washed cells)" % len(banded),
        len(banded) == 0,
    )

# Signs themselves must be the unicode minus / plus at the shared column.
sign_col = info["del"]["sign"]
del_sign = info["del"]["cells"][sign_col]["char"] if sign_col >= 0 else ""
add_sign = info["add"]["cells"][sign_col]["char"] if sign_col >= 0 else ""
check("del sign is U+2212 MINUS (%r)" % del_sign, del_sign == "\u2212")
check("add sign is + (%r)" % add_sign, add_sign == "+")

sys.exit(0 if ok else 1)
PY
check "diff cell geometry" $?

echo
if [ "$fail" -eq 0 ]; then echo "smoke-diff: ok"; else echo "smoke-diff: FAILURES"; fi
exit "$fail"
