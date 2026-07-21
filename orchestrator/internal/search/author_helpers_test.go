package search

import (
	"reflect"
	"testing"
)

func TestAppendUniqueAuthor(t *testing.T) {
	authors := make([]string, 0, 4)
	seen := make(map[string]struct{})

	authors = appendUniqueAuthor(authors, seen, "  Alice  ")
	authors = appendUniqueAuthor(authors, seen, "alice")
	authors = appendUniqueAuthor(authors, seen, " ")
	authors = appendUniqueAuthor(authors, seen, "Bob")

	want := []string{"Alice", "Bob"}
	if !reflect.DeepEqual(authors, want) {
		t.Fatalf("unexpected authors: got %#v want %#v", authors, want)
	}
}

func TestParseDelimitedAuthors(t *testing.T) {
	t.Run("returns nil for blank input", func(t *testing.T) {
		if got := parseDelimitedAuthors("   "); got != nil {
			t.Fatalf("expected nil for blank input, got %#v", got)
		}
	})

	t.Run("returns nil for delimiter-only input", func(t *testing.T) {
		if got := parseDelimitedAuthors(";,|\n\r"); got != nil {
			t.Fatalf("expected nil for delimiter-only input, got %#v", got)
		}
	})

	t.Run("returns nil when all split tokens are blank", func(t *testing.T) {
		if got := parseDelimitedAuthors(" ; ; "); got != nil {
			t.Fatalf("expected nil for blank split tokens, got %#v", got)
		}
	})

	t.Run("splits and deduplicates delimiters", func(t *testing.T) {
		got := parseDelimitedAuthors(" Alice;Bob,carol|Bob\nDana\rALICE ")
		want := []string{"Alice", "Bob", "carol", "Dana"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected authors: got %#v want %#v", got, want)
		}
	})
}

func TestParseDBLPAuthors(t *testing.T) {
	t.Run("handles strings and nested collections", func(t *testing.T) {
		got := parseDBLPAuthors([]any{
			" Alice ",
			[]string{"Bob", "alice"},
			[]any{
				map[string]any{"text": "Carol"},
				map[string]any{"#text": "Dana"},
				map[string]any{"name": "Eve"},
				map[string]any{"ignored": "value"},
			},
		})
		want := []string{"Alice", "Bob", "Carol", "Dana", "Eve"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected authors: got %#v want %#v", got, want)
		}
	})

	t.Run("returns nil when nothing usable is present", func(t *testing.T) {
		if got := parseDBLPAuthors([]any{nil, map[string]any{"ignored": "value"}, " "}); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})
}

func TestCountNonEmptyStrings(t *testing.T) {
	if got, want := countNonEmptyStrings([]string{"", "  a ", " ", "b"}), 2; got != want {
		t.Fatalf("unexpected count: got %d want %d", got, want)
	}
}
