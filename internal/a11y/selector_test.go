package goa11y

import "testing"

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in   string
		segs int
		role Role
		nth  int
	}{
		{"button", 1, RoleButton, 0},
		{"button[name=\"OK\"]", 1, RoleButton, 0},
		{"dialog > button", 2, RoleButton, 0},
		{"window button", 2, RoleButton, 0},
		{"*[name='x']", 1, "", 0},
		{"list_item:nth(2)", 1, RoleListItem, 2},
		{"group > * > button", 3, RoleButton, 0},
	}
	for _, c := range cases {
		s, err := ParseSelector(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if len(s.Segments) != c.segs {
			t.Errorf("%q: got %d segs want %d", c.in, len(s.Segments), c.segs)
		}
		last := s.Segments[len(s.Segments)-1].Simple
		if last.Role != c.role {
			t.Errorf("%q: role %q want %q", c.in, last.Role, c.role)
		}
		if last.Nth != c.nth {
			t.Errorf("%q: nth %d want %d", c.in, last.Nth, c.nth)
		}
	}
}

func TestMatchAttrs(t *testing.T) {
	e := &ElementData{Role: RoleButton, Name: "Export", Value: "v1"}
	s, _ := ParseSelector(`button[name^="Exp"]`)
	if !MatchesSimple(e, &s.Segments[0].Simple) {
		t.Error("prefix match failed")
	}
	s2, _ := ParseSelector(`button[name*="por"]`)
	if !MatchesSimple(e, &s2.Segments[0].Simple) {
		t.Error("contains match failed")
	}
	s3, _ := ParseSelector(`button[name="Nope"]`)
	if MatchesSimple(e, &s3.Segments[0].Simple) {
		t.Error("should not match")
	}
}
