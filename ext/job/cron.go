package job

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSched is a 5-field cron: min hour dom month dow (local time).
// Supports: *, N, N-M, */N, N,M lists. Dow: 0-6 or 7 (Sunday).
type cronSched struct {
	min, hour, dom, mon, dow fieldSet
}

type fieldSet struct {
	// any is true for *
	any bool
	// vals is the allowed set (empty if any)
	vals map[int]bool
}

func parseCron(expr string) (*cronSched, error) {
	expr = strings.TrimSpace(expr)
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron: want 5 fields, got %d (%q)", len(parts), expr)
	}
	min, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron min: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron hour: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron dom: %w", err)
	}
	mon, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron month: %w", err)
	}
	dow, err := parseField(parts[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("cron dow: %w", err)
	}
	// Normalize 7 → 0 (Sunday)
	if dow.vals != nil && dow.vals[7] {
		dow.vals[0] = true
		delete(dow.vals, 7)
	}
	return &cronSched{min: min, hour: hour, dom: dom, mon: mon, dow: dow}, nil
}

func parseField(s string, lo, hi int) (fieldSet, error) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return fieldSet{any: true}, nil
	}
	out := fieldSet{vals: map[int]bool{}}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// */n or a-b/n
		step := 1
		rangePart := part
		if i := strings.Index(part, "/"); i >= 0 {
			rangePart = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return fieldSet{}, fmt.Errorf("bad step in %q", part)
			}
			step = n
		}
		var a, b int
		if rangePart == "*" {
			a, b = lo, hi
		} else if j := strings.Index(rangePart, "-"); j >= 0 {
			var err error
			a, err = strconv.Atoi(rangePart[:j])
			if err != nil {
				return fieldSet{}, err
			}
			b, err = strconv.Atoi(rangePart[j+1:])
			if err != nil {
				return fieldSet{}, err
			}
		} else {
			n, err := strconv.Atoi(rangePart)
			if err != nil {
				return fieldSet{}, err
			}
			a, b = n, n
		}
		if a < lo || b > hi || a > b {
			return fieldSet{}, fmt.Errorf("out of range %d-%d in %q", a, b, part)
		}
		for v := a; v <= b; v += step {
			out.vals[v] = true
		}
	}
	if len(out.vals) == 0 {
		return fieldSet{}, fmt.Errorf("empty field %q", s)
	}
	return out, nil
}

func (f fieldSet) match(v int) bool {
	if f.any {
		return true
	}
	return f.vals[v]
}

func (c *cronSched) match(t time.Time) bool {
	// Use local time for operator-friendly schedules.
	t = t.Local()
	if !c.min.match(t.Minute()) || !c.hour.match(t.Hour()) {
		return false
	}
	if !c.mon.match(int(t.Month())) {
		return false
	}
	domOK := c.dom.match(t.Day())
	dowOK := c.dow.match(int(t.Weekday())) // Sunday=0
	// Standard: if both dom and dow are restricted, either may match (OR).
	if !c.dom.any && !c.dow.any {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// nextAfter returns the next minute strictly after t that matches (search up to 2 years).
func (c *cronSched) nextAfter(t time.Time) (time.Time, error) {
	t = t.Local().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(2, 0, 0)
	for !t.After(limit) {
		if c.match(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron: no match within 2 years")
}
