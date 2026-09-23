package goal

import "time"

// WorkerRecord is one bounded child/worktree attempt. It is status metadata,
// not a transcript; detailed evidence remains in Facts and the event log.
type WorkerRecord struct {
	ID        string    `json:"id"`
	Mode      string    `json:"mode,omitempty"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Summary   string    `json:"summary,omitempty"`
}

type VerificationRecord struct {
	Status    string    `json:"status,omitempty"` // pending | passed | failed
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func (st *State) SetPhase(phase string) { st.Phase = boundedText(phase, 80) }
func (st *State) RecordWorker(w WorkerRecord) {
	w.ID = boundedText(w.ID, 80)
	w.Mode = boundedText(w.Mode, 40)
	w.Status = boundedText(w.Status, 40)
	w.Summary = boundedText(w.Summary, 1000)
	for i := range st.Workers {
		if st.Workers[i].ID == w.ID {
			st.Workers[i] = w
			return
		}
	}
	if len(st.Workers) >= 128 {
		st.Workers = st.Workers[len(st.Workers)-127:]
	}
	st.Workers = append(st.Workers, w)
}
func (st *State) SetVerification(status, note string) {
	st.Verification = VerificationRecord{Status: boundedText(status, 40), Note: boundedText(note, 2000), UpdatedAt: time.Now().UTC()}
}
func boundedText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
