package goal

import "strings"

const (
	maxPlanItems         = 64
	maxFacts             = 256
	maxEvidencePerReport = 32
	maxStateTextRunes    = 32_000
	maxGoalJSONBytes     = 4 << 20
)

// sanitizeState clamps durable fields so one corrupted or over-large goal file
// cannot grow without bound or blow prompt assembly.
func sanitizeState(st *State) {
	if st == nil {
		return
	}
	switch st.Status {
	case StatusPending, StatusRunning, StatusDone, StatusFailed, StatusPartial, StatusBlocked, "":
		// keep; empty treated as pending by hosts
	default:
		st.Status = StatusPending
	}
	st.Goal = truncateRunes(strings.TrimSpace(st.Goal), maxStateTextRunes)
	st.Summary = truncateRunes(st.Summary, maxStateTextRunes)
	st.LastReply = truncateRunes(st.LastReply, maxStateTextRunes)
	st.Error = truncateRunes(st.Error, maxStateTextRunes)
	st.Partial = truncateRunes(st.Partial, maxStateTextRunes)
	st.Question = truncateRunes(st.Question, maxStateTextRunes)
	st.VerifyNote = truncateRunes(st.VerifyNote, maxStateTextRunes)
	st.SessionID = truncateRunes(st.SessionID, 256)
	st.CurrentItem = truncateRunes(st.CurrentItem, 128)
	st.Workspace = truncateRunes(st.Workspace, 4096)
	if st.Step < 0 {
		st.Step = 0
	}
	if st.MaxSteps < 0 {
		st.MaxSteps = 0
	}
	if st.MaxSteps > MaxMaxSteps {
		st.MaxSteps = MaxMaxSteps
	}
	if st.RetryCount < 0 {
		st.RetryCount = 0
	}
	if len(st.Plan.Items) > maxPlanItems {
		st.Plan.Items = st.Plan.Items[:maxPlanItems]
	}
	for i := range st.Plan.Items {
		st.Plan.Items[i].ID = truncateRunes(st.Plan.Items[i].ID, 128)
		st.Plan.Items[i].Title = truncateRunes(st.Plan.Items[i].Title, 512)
		st.Plan.Items[i].Note = truncateRunes(st.Plan.Items[i].Note, 2000)
		switch st.Plan.Items[i].Status {
		case ItemPending, ItemDone, ItemSkipped, ItemFailed, "":
		default:
			st.Plan.Items[i].Status = ItemPending
		}
	}
	if len(st.Facts) > maxFacts {
		st.Facts = st.Facts[len(st.Facts)-maxFacts:]
	}
	for i := range st.Facts {
		st.Facts[i].Claim = truncateRunes(st.Facts[i].Claim, 4000)
		st.Facts[i].Source = truncateRunes(st.Facts[i].Source, 512)
		st.Facts[i].ID = truncateRunes(st.Facts[i].ID, 128)
		if st.Facts[i].Confidence < 0 {
			st.Facts[i].Confidence = 0
		}
		if st.Facts[i].Confidence > 1 {
			st.Facts[i].Confidence = 1
		}
	}
}

func capEvidence(in []Fact) []Fact {
	if len(in) <= maxEvidencePerReport {
		return in
	}
	return in[:maxEvidencePerReport]
}
