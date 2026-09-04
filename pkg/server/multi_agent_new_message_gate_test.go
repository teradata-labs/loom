package server

import "testing"

// The four roles below must all produce a SessionUpdate_NewMessage — this
// mirrors the exact condition at multi_agent.go:4721. skill_body and
// hygiene_injection are synthetic content that still needs to reach a live
// streaming consumer with their correct (non-"user") role.
func TestNewMessageUpdateGate_IncludesSyntheticRoles(t *testing.T) {
	included := []string{"assistant", "user", "tool", "skill_body", "hygiene_injection"}
	for _, role := range included {
		if !isNewMessageUpdateRole(role) {
			t.Errorf("role %q should produce a SessionUpdate_NewMessage", role)
		}
	}
	if isNewMessageUpdateRole("system") {
		t.Errorf("role \"system\" should NOT produce a SessionUpdate_NewMessage (unchanged behavior)")
	}
}
