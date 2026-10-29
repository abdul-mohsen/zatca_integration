package queue

import "testing"

func TestBranchDocumentSubjectsExcludeOnboarding(t *testing.T) {
	subjects := BranchDocumentSubjects("tenant_db", 7)
	want := []string{
		"zatca.bill.tenant_db.7",
		"zatca.credit.tenant_db.7",
		"zatca.debit.tenant_db.7",
	}
	if len(subjects) != len(want) {
		t.Fatalf("got %d subjects, want %d", len(subjects), len(want))
	}
	for i := range want {
		if subjects[i] != want[i] {
			t.Errorf("subject[%d] = %q, want %q", i, subjects[i], want[i])
		}
	}
}
