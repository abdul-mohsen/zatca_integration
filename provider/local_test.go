package provider

import "testing"

func TestParseSubmitScenario(t *testing.T) {
	for _, raw := range []string{"", "accepted", " WARNING ", "rejected", "timeout", "technical", "conflict", "crash_after_acceptance"} {
		if _, err := ParseSubmitScenario(raw); err != nil {
			t.Errorf("ParseSubmitScenario(%q) error: %v", raw, err)
		}
	}
	if _, err := ParseSubmitScenario("reconcile_accepted"); err == nil {
		t.Fatal("reconcile scenario should not be accepted as a submit scenario")
	}
}
