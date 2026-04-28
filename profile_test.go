package linkedin

import (
	"net/url"
	"strings"
	"testing"
)

// TestBuildProfileURL_EscapesUTF8 pins the contract that vanity names with
// non-ASCII characters are percent-encoded before being interpolated into
// the GraphQL variables query string. LinkedIn's edge returns HTTP 400 on
// raw UTF-8 bytes — we observed this repeatedly in production runs against
// names like "oscar-mayorquín" and "ramónlópezsotoyarritu".
func TestBuildProfileURL_EscapesUTF8(t *testing.T) {
	c := New(Auth{LiAt: "x", CSRF: "y"})

	cases := []struct {
		name, vanity, mustContain string
	}{
		{"ascii passthrough", "satyanadella", "vanityName:satyanadella"},
		{"latin diacritic", "oscar-mayorquín", "vanityName:" + url.QueryEscape("oscar-mayorquín")},
		{"multi diacritic", "ramónlópezsotoyarritu", "vanityName:" + url.QueryEscape("ramónlópezsotoyarritu")},
		{"hebrew script", "izhak-zivion-יצחק-צביון-5251a4b", "vanityName:" + url.QueryEscape("izhak-zivion-יצחק-צביון-5251a4b")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.buildProfileURL(tc.vanity)
			if !strings.Contains(got, tc.mustContain) {
				t.Errorf("buildProfileURL(%q) = %q, missing %q", tc.vanity, got, tc.mustContain)
			}
			// And the URL must parse — i.e. it must be a valid URL after we
			// hand it to net/http.NewRequest. Catch any unescaped grammar
			// regression here.
			if _, err := url.Parse(got); err != nil {
				t.Errorf("buildProfileURL(%q) produced unparseable URL: %v", tc.vanity, err)
			}
		})
	}
}
