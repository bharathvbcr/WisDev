package cli

import "testing"

func TestRemapDOICitations(t *testing.T) {
	refs := []docGenReference{
		{title: "A", link: "https://doi.org/10.1038/s41591-024-03097-1"},
		{title: "B", link: "10.1056/aioa2300068"},
	}
	doiRef := doiRefMap(refs)
	cases := map[string]string{
		"Grounding improves accuracy [10.1038/s41591-024-03097-1].":                  "Grounding improves accuracy [1].",
		"A trial reported gains [10.1056/aioa2300068].":                              "A trial reported gains [2].",
		"An unmapped fragment [1186/s12909] leaks here.":                             "An unmapped fragment leaks here.",
		"Numeric markers [2] and [1, 3] are untouched.":                              "Numeric markers [2] and [1, 3] are untouched.",
		"Multiple [10.1038/s41591-024-03097-1] and [10.1056/aioa2300068] hits.":      "Multiple [1] and [2] hits.",
		"Grouped [10.1038/s41591-024-03097-1, 10.1056/aioa2300068] in one bracket.":  "Grouped [1, 2] in one bracket.",
		"Group with one unresolved [10.1038/s41591-024-03097-1; 10.9999/nope] here.": "Group with one unresolved [1] here.",
	}
	for in, want := range cases {
		if got := remapDOICitations(in, doiRef); got != want {
			t.Errorf("remapDOICitations(%q)\n  got  %q\n  want %q", in, got, want)
		}
	}
}
