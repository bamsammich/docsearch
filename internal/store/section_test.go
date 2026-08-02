package store

import "testing"

// The Go half of the precision suite that guards SectionMatchSQL. The Python
// half is tests/test_section_resolution.py; one rule in two languages needs
// two test suites, and these are deliberately the same five cases.
//
// Every assertion here is about what must NOT match. A coverage metric cannot
// police this: over-matching increases the number of resolved joins, so it
// moves the reassuring way while results get worse.

var fixture = []string{"4", "4.1", "4.2", "4.10", "41", "41.11", "43", "43.6", "43.6.1", "5"}

func covered(ref string) []string {
	var out []string
	for _, s := range fixture {
		if SectionCovers(ref, s) {
			out = append(out, s)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChapterReferenceCoversOnlyItsOwnSubtree(t *testing.T) {
	want := []string{"4", "4.1", "4.2", "4.10"}
	if got := covered("4"); !equal(got, want) {
		t.Fatalf("covered(%q) = %v, want %v", "4", got, want)
	}
}

func TestChapterReferenceDoesNotReachNumericallySimilarChapters(t *testing.T) {
	got := covered("4")
	for _, forbidden := range []string{"41", "41.11", "43", "43.6", "43.6.1"} {
		for _, s := range got {
			if s == forbidden {
				t.Errorf("reference to chapter 4 resolved into %q; "+
					"the match must be component-wise, not a string prefix", forbidden)
			}
		}
	}
}

func TestDeepReferenceIsExact(t *testing.T) {
	if got, want := covered("43.6"), []string{"43.6", "43.6.1"}; !equal(got, want) {
		t.Errorf("covered(43.6) = %v, want %v", got, want)
	}
	if got, want := covered("43.6.1"), []string{"43.6.1"}; !equal(got, want) {
		t.Errorf("covered(43.6.1) = %v, want %v", got, want)
	}
}

func TestTwoDigitChapterDoesNotAbsorbItsOwnPrefix(t *testing.T) {
	if got, want := covered("41"), []string{"41", "41.11"}; !equal(got, want) {
		t.Errorf("covered(41) = %v, want %v", got, want)
	}
}

func TestLeafReferenceResolvesToItselfOnly(t *testing.T) {
	if got, want := covered("5"), []string{"5"}; !equal(got, want) {
		t.Errorf("covered(5) = %v, want %v", got, want)
	}
	if got, want := covered("4.1"), []string{"4.1"}; !equal(got, want) {
		t.Errorf("covered(4.1) = %v, want %v", got, want)
	}
}

func TestEmptyReferenceCoversNothing(t *testing.T) {
	if got := covered(""); len(got) != 0 {
		t.Errorf("empty reference covered %v, want nothing", got)
	}
}
