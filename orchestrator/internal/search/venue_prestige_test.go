package search

import "testing"

func TestVenuePrestigeNorm(t *testing.T) {
	cases := []struct {
		venue string
		want  float64
	}{
		{"Nature", 1.0},
		{"Nature Communications", 0.80},
		{"Science Advances", 0.78},
		{"arXiv preprint", 0.60},
		{"Unknown Journal", 0.50},
		{"", 0.50},
	}
	for _, tc := range cases {
		got := VenuePrestigeNorm(tc.venue)
		if got != tc.want {
			t.Fatalf("VenuePrestigeNorm(%q)=%v want %v", tc.venue, got, tc.want)
		}
	}
}

func TestAccessNorm(t *testing.T) {
	if AccessNorm("https://oa.example", "") != 1.0 {
		t.Fatal("expected open access URL to score 1.0")
	}
	if AccessNorm("", "https://pdf.example") != 1.0 {
		t.Fatal("expected pdf URL to score 1.0")
	}
	if AccessNorm("", "") != 0.30 {
		t.Fatal("expected closed access to score 0.30")
	}
}
