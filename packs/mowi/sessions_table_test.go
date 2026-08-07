package mowi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow"
)

func sessionFixtures(n int, now time.Time) []mow.SessionInfo {
	out := make([]mow.SessionInfo, 0, n)
	for i := range n {
		out = append(out, mow.SessionInfo{
			ID:      fmt.Sprintf("2026%04d-1015%02d", i, i),
			Updated: now.Add(-time.Duration(i) * time.Hour),
			Preview: strings.Repeat("prompt ", i%5+1),
		})
	}
	return out
}

// Every session in the listing must get a row. The table's inner viewport
// renders only a window and silently drops the rest when it is too short, so a
// height off by one loses the oldest session with no error anywhere.
func TestSessionsTableRendersEveryRow(t *testing.T) {
	now := time.Now()
	for _, n := range []int{1, 2, 3, 4, 7, 12, 20} {
		infos := sessionFixtures(n, now)
		out := stripANSI(m100().sessionsTable(infos, "", now))
		for _, s := range infos {
			if !strings.Contains(out, s.ID) {
				t.Fatalf("n=%d: session %q missing from listing:\n%s", n, s.ID, out)
			}
		}
	}
}

// Beyond the cap the listing reports a remainder instead of flooding the
// transcript, and the rows it does show are the newest ones.
func TestSessionsTableCapsAndReportsRemainder(t *testing.T) {
	now := time.Now()
	infos := sessionFixtures(sessionsMaxShow+5, now)
	out := stripANSI(m100().sessionsTable(infos, "", now))
	if !strings.Contains(out, "… 5 more") {
		t.Fatalf("missing remainder note:\n%s", out)
	}
	if !strings.Contains(out, infos[0].ID) {
		t.Fatalf("newest session dropped:\n%s", out)
	}
	// Rows past the cap must not render.
	if strings.Contains(out, infos[sessionsMaxShow].ID) {
		t.Fatalf("row past cap rendered:\n%s", out)
	}
}

// The active session is marked so a user can tell which one they are in.
func TestSessionsTableMarksCurrent(t *testing.T) {
	now := time.Now()
	infos := sessionFixtures(3, now)
	out := stripANSI(m100().sessionsTable(infos, infos[1].ID, now))
	var marked string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, infos[1].ID) {
			marked = line
		}
	}
	if marked == "" {
		t.Fatalf("current session row not found:\n%s", out)
	}
	if !strings.Contains(marked, glyphBullet) {
		t.Fatalf("current session row not marked: %q", marked)
	}
}

// The table itself must fit the terminal: a table wider than the viewport
// soft-wraps and destroys the column alignment it exists to provide. (The
// prose header/footer lines wrap like any other transcript text.)
func TestSessionsTableFitsWidth(t *testing.T) {
	now := time.Now()
	infos := sessionFixtures(6, now)
	for _, w := range []int{minTermWidth, 60, 80, 100, 200} {
		m := &model{theme: newTheme(), width: w}
		out := stripANSI(m.sessionsTable(infos, "", now))
		for _, line := range strings.Split(out, "\n") {
			// Table rows only: the rule, the header row, and id rows. Prose
			// lines (title, remainder note, resume hint) wrap like any other
			// transcript text and are not width-managed here.
			isRule := strings.Contains(line, "───")
			isRow := strings.Contains(line, "-1015")
			isHead := strings.Contains(line, "updated") && strings.Contains(line, "preview")
			if !isRule && !isRow && !isHead {
				continue
			}
			if got := len([]rune(line)); got > w {
				t.Fatalf("width=%d: table line of %d runes overflows:\n%q", w, got, line)
			}
		}
	}
}

func m100() *model { return &model{theme: newTheme(), width: 100} }
