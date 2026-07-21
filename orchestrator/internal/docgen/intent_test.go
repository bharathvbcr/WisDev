package docgen

import "testing"

func TestParseIntent(t *testing.T) {
	cases := []struct {
		raw  string
		want Intent
	}{
		{"", IntentFullPaper},
		{"fullpaper", IntentFullPaper},
		{"full-paper", IntentFullPaper},
		{"manuscript", IntentFullPaper},
		{"report", IntentReport},
		{"quick-report", IntentReport},
		{"litreview", IntentLitReview},
		{"literature-review", IntentLitReview},
		{"review", IntentLitReview},
	}
	for _, c := range cases {
		got, err := ParseIntent(c.raw)
		if err != nil {
			t.Fatalf("ParseIntent(%q) error: %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("ParseIntent(%q)=%q want %q", c.raw, got, c.want)
		}
	}
	if _, err := ParseIntent("thesis"); err == nil {
		t.Fatal("expected error for unknown intent")
	}
}

func TestIntentDisplayName(t *testing.T) {
	if IntentReport.DisplayName() != "Quick Report" {
		t.Errorf("report display name: %q", IntentReport.DisplayName())
	}
	if IntentLitReview.DisplayName() != "Literature Review" {
		t.Errorf("litreview display name: %q", IntentLitReview.DisplayName())
	}
	if IntentFullPaper.DisplayName() != "Full Paper" {
		t.Errorf("fullpaper display name: %q", IntentFullPaper.DisplayName())
	}
}
