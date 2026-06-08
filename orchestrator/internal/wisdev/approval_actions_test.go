package wisdev

import "testing"

func TestCanonicalizeConfirmationAction(t *testing.T) {
	t.Run("normalizes legacy aliases", func(t *testing.T) {
		cases := map[string]string{
			"confirm_and_execute": "approve",
			"cancel":              "skip",
			"reject_replan":       "reject_and_replan",
			"reject_and_replan":   "reject_and_replan",
		}
		for input, want := range cases {
			if got := CanonicalizeConfirmationAction(input); got != want {
				t.Fatalf("CanonicalizeConfirmationAction(%q) = %q, want %q", input, got, want)
			}
		}
	})

	t.Run("returns defensive copy of allowed actions", func(t *testing.T) {
		actions := ConfirmationActions()
		if len(actions) != 4 {
			t.Fatalf("expected 4 actions, got %d", len(actions))
		}
		actions[0] = "mutated"
		fresh := ConfirmationActions()
		if fresh[0] != "approve" {
			t.Fatalf("expected canonical actions to be immutable, got %q", fresh[0])
		}
	})
}
