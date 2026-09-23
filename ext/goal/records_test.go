package goal

import (
	"strings"
	"testing"
	"time"
)

func TestPhaseWorkerAndVerificationRecordsBounded(t *testing.T) {
	var st State
	st.SetPhase(strings.Repeat("p", 100))
	st.SetVerification("failed", strings.Repeat("n", 3000))
	if len([]rune(st.Phase)) > 81 || len([]rune(st.Verification.Note)) > 2001 {
		t.Fatal("record bounds")
	}
	for i := range 140 {
		st.RecordWorker(WorkerRecord{ID: string(rune(1000 + i)), Status: "done", StartedAt: time.Now()})
	}
	if len(st.Workers) != 128 {
		t.Fatalf("workers=%d", len(st.Workers))
	}
	id := st.Workers[0].ID
	st.RecordWorker(WorkerRecord{ID: id, Status: "failed"})
	if len(st.Workers) != 128 || st.Workers[0].Status != "failed" {
		t.Fatal("worker update")
	}
}
