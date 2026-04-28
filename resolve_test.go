package linkedin

import "testing"

// TestStripURNPrefix locks in the contract that resolved URNs are emitted as
// bare LinkedIn entity IDs. We empirically verified by issuing geo-filtered
// SearchPeople requests that the Voyager search filter silently ignores any
// URN-prefixed value (e.g. "urn:li:geo:90000084") and only narrows results
// when the bare ID is supplied. If this test ever needs to be relaxed,
// re-verify the search filter behaviour first.
func TestStripURNPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"90000084", "90000084"},
		{"urn:li:geo:90000084", "90000084"},
		{"urn:li:fsd_geo:90000084", "90000084"},
		{"urn:li:fsd_company:1441", "1441"},
		{"urn:li:fsd_school:1792", "1792"},
		{"urn:li:fs_geo:(90000084)", "90000084"},
	}
	for _, tc := range cases {
		got := stripURNPrefix(tc.in)
		if got != tc.want {
			t.Errorf("stripURNPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
