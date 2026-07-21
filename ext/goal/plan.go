package goal

import (
	"fmt"
	"strings"
	"unicode"
)

// PlanItemStatus is the lifecycle of one checklist item.
type PlanItemStatus string

const (
	ItemPending PlanItemStatus = "pending"
	ItemDone    PlanItemStatus = "done"
	ItemFailed  PlanItemStatus = "failed"
	ItemSkipped PlanItemStatus = "skipped"
)

// PlanItem is one unit of work inside a goal checklist.
type PlanItem struct {
	ID     string         `json:"id"`
	Title  string         `json:"title"`
	Status PlanItemStatus `json:"status"`
	Note   string         `json:"note,omitempty"`
}

// Plan is an ordered checklist. Empty plan = legacy free-form goal (any done is OK).
type Plan struct {
	Items []PlanItem `json:"items,omitempty"`
}

// HasItems reports whether a checklist is in use.
func (p *Plan) HasItems() bool {
	return p != nil && len(p.Items) > 0
}

// AllTerminal reports every item is done, failed, or skipped.
func (p *Plan) AllTerminal() bool {
	if !p.HasItems() {
		return true
	}
	for _, it := range p.Items {
		switch it.Status {
		case ItemDone, ItemFailed, ItemSkipped:
		default:
			return false
		}
	}
	return true
}

// AllDone reports every item is done or skipped (no failed, no pending).
func (p *Plan) AllDone() bool {
	if !p.HasItems() {
		return true
	}
	for _, it := range p.Items {
		switch it.Status {
		case ItemDone, ItemSkipped:
		default:
			return false
		}
	}
	return true
}

// AnyFailed reports any item failed.
func (p *Plan) AnyFailed() bool {
	if p == nil {
		return false
	}
	for _, it := range p.Items {
		if it.Status == ItemFailed {
			return true
		}
	}
	return false
}

// NextPending returns the first pending item, or false.
func (p *Plan) NextPending() (PlanItem, bool) {
	if p == nil {
		return PlanItem{}, false
	}
	for _, it := range p.Items {
		if it.Status == ItemPending || it.Status == "" {
			return it, true
		}
	}
	return PlanItem{}, false
}

// SetItem updates one item by id. Empty status leaves status unchanged.
func (p *Plan) SetItem(id, status, note string) error {
	if p == nil {
		return fmt.Errorf("nil plan")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("empty item id")
	}
	st := PlanItemStatus(strings.ToLower(strings.TrimSpace(status)))
	if st != "" && st != ItemPending && st != ItemDone && st != ItemFailed && st != ItemSkipped {
		return fmt.Errorf("bad item status %q", status)
	}
	for i := range p.Items {
		if p.Items[i].ID != id {
			continue
		}
		if st != "" {
			p.Items[i].Status = st
		}
		if n := strings.TrimSpace(note); n != "" {
			p.Items[i].Note = n
		}
		return nil
	}
	return fmt.Errorf("unknown plan item %q", id)
}

// ReplaceItems sets the checklist (normalizes ids/titles/status).
func (p *Plan) ReplaceItems(items []PlanItem) error {
	if p == nil {
		return fmt.Errorf("nil plan")
	}
	if len(items) == 0 {
		p.Items = nil
		return nil
	}
	out := make([]PlanItem, 0, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		id := strings.TrimSpace(it.ID)
		title := strings.TrimSpace(it.Title)
		if id == "" {
			id = slugItemID(title, i)
		}
		if title == "" {
			title = id
		}
		if seen[id] {
			return fmt.Errorf("duplicate plan item id %q", id)
		}
		seen[id] = true
		st := PlanItemStatus(strings.ToLower(strings.TrimSpace(string(it.Status))))
		if st == "" {
			st = ItemPending
		}
		switch st {
		case ItemPending, ItemDone, ItemFailed, ItemSkipped:
		default:
			return fmt.Errorf("bad status %q on item %q", it.Status, id)
		}
		out = append(out, PlanItem{
			ID:     id,
			Title:  title,
			Status: st,
			Note:   strings.TrimSpace(it.Note),
		})
	}
	p.Items = out
	return nil
}

// Format returns a short checklist for prompts.
func (p *Plan) Format() string {
	if !p.HasItems() {
		return "(no checklist yet)"
	}
	var b strings.Builder
	for _, it := range p.Items {
		st := it.Status
		if st == "" {
			st = ItemPending
		}
		fmt.Fprintf(&b, "- [%s] %s (%s)", st, it.Title, it.ID)
		if it.Note != "" {
			fmt.Fprintf(&b, " — %s", it.Note)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func slugItemID(title string, i int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
				b.WriteByte('-')
			}
		}
		if b.Len() >= 24 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return fmt.Sprintf("item-%d", i+1)
	}
	return s
}
