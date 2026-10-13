package goa11y

import (
	"fmt"
	"strings"
)

// Combinator links selector segments.
type Combinator string

const (
	CombinatorRoot       Combinator = "root"
	CombinatorChild      Combinator = "child"      // " > "
	CombinatorDescendant Combinator = "descendant" // " "
)

// MatchOp is an attribute comparison operator.
type MatchOp string

const (
	OpEqual         MatchOp = "="
	OpContains      MatchOp = "*="
	OpStartsWith    MatchOp = "^="
	OpEndsWith      MatchOp = "$="
	OpNotEqual      MatchOp = "!="
	OpNotContains   MatchOp = "!*="
	OpNotStartsWith MatchOp = "!^="
	OpNotEndsWith   MatchOp = "!$="
)

// AttrFilter is one [attr op "value"] predicate.
type AttrFilter struct {
	Attr  string
	Op    MatchOp
	Value string
}

// SimpleSelector is (role | "*")? attr_filter* pseudo?.
type SimpleSelector struct {
	Role    Role // empty = any (universal)
	Nth     int  // 0 = none; 1-based :nth(n)
	Attrs   []AttrFilter
}

// SelectorSegment is one step in a selector chain.
type SelectorSegment struct {
	Combinator Combinator
	Simple     SimpleSelector
}

// Selector is a parsed selector (single clause).
type Selector struct {
	Segments []SelectorSegment
}

// SelectorGroup is a comma-separated alternation of selectors.
type SelectorGroup struct {
	Selectors []Selector
}

// ParseSelector parses a CSS-like selector string.
func ParseSelector(input string) (*Selector, error) {
	s := &Selector{}
	rest := strings.TrimSpace(input)
	if rest == "" {
		return nil, &Error{Kind: ErrInvalidSelector, Selector: input, Message: "empty selector"}
	}
	// First segment has Root combinator.
	combinator := CombinatorRoot
	first := true
	for len(rest) > 0 {
		if !first {
			// parse combinator
			trimmed := strings.TrimLeft(rest, " \t")
			if strings.HasPrefix(trimmed, ">") {
				combinator = CombinatorChild
				rest = strings.TrimLeft(trimmed[1:], " \t")
			} else if len(trimmed) < len(rest) {
				combinator = CombinatorDescendant
				rest = trimmed
			} else {
				return nil, &Error{Kind: ErrInvalidSelector, Selector: input, Message: "expected combinator"}
			}
		}
		simple, consumed, err := parseSimple(rest)
		if err != nil {
			return nil, &Error{Kind: ErrInvalidSelector, Selector: input, Message: err.Error()}
		}
		s.Segments = append(s.Segments, SelectorSegment{Combinator: combinator, Simple: simple})
		rest = rest[consumed:]
		first = false
	}
	if len(s.Segments) == 0 {
		return nil, &Error{Kind: ErrInvalidSelector, Selector: input, Message: "no segments"}
	}
	return s, nil
}

func parseSimple(s string) (SimpleSelector, int, error) {
	var out SimpleSelector
	i := 0
	// role or "*"
	if i < len(s) && s[i] == '*' {
		i++
	} else {
		start := i
		for i < len(s) && (isLower(s[i]) || s[i] == '_') {
			i++
		}
		if i > start {
			out.Role = Role(s[start:i])
		}
	}
	// attr filters
	for i < len(s) && s[i] == '[' {
		end := strings.IndexByte(s[i:], ']')
		if end < 0 {
			return out, 0, fmt.Errorf("unterminated attribute filter")
		}
		inner := s[i+1 : i+end]
		f, err := parseAttr(inner)
		if err != nil {
			return out, 0, err
		}
		out.Attrs = append(out.Attrs, f)
		i += end + 1
	}
	// pseudo :nth(n)
	if strings.HasPrefix(s[i:], ":nth(") {
		close := strings.IndexByte(s[i:], ')')
		if close < 0 {
			return out, 0, fmt.Errorf("unterminated :nth(")
		}
		var n int
		if _, err := fmt.Sscanf(s[i+5:i+close], "%d", &n); err != nil || n < 1 {
			return out, 0, fmt.Errorf("bad :nth() argument")
		}
		out.Nth = n
		i += close + 1
	}
	return out, i, nil
}

func parseAttr(inner string) (AttrFilter, error) {
	inner = strings.TrimSpace(inner)
	// op detection (longest first)
	for _, op := range []MatchOp{OpNotContains, OpNotStartsWith, OpNotEndsWith, OpContains, OpStartsWith, OpEndsWith, OpNotEqual, OpEqual} {
		if idx := strings.Index(string(inner), string(op)); idx >= 0 {
			attr := strings.TrimSpace(inner[:idx])
			val := strings.TrimSpace(inner[idx+len(op):])
			val = strings.Trim(val, `"'`)
			if attr == "" {
				return AttrFilter{}, fmt.Errorf("empty attribute name")
			}
			return AttrFilter{Attr: attr, Op: op, Value: val}, nil
		}
	}
	return AttrFilter{}, fmt.Errorf("no operator in [%s]", inner)
}

func isLower(b byte) bool { return b >= 'a' && b <= 'z' }

// ParseSelectorGroup parses a comma-separated group.
func ParseSelectorGroup(input string) (*SelectorGroup, error) {
	parts := splitTopLevelCommas(input)
	g := &SelectorGroup{}
	for _, p := range parts {
		sel, err := ParseSelector(p)
		if err != nil {
			return nil, err
		}
		g.Selectors = append(g.Selectors, *sel)
	}
	if len(g.Selectors) == 0 {
		return nil, &Error{Kind: ErrInvalidSelector, Selector: input, Message: "empty group"}
	}
	return g, nil
}

func splitTopLevelCommas(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

// MatchesSimple reports whether an element matches a simple selector.
func MatchesSimple(e *ElementData, s *SimpleSelector) bool {
	if s.Role != "" && e.Role != s.Role {
		return false
	}
	for _, f := range s.Attrs {
		if !matchAttr(f, e) {
			return false
		}
	}
	return true
}

func matchAttr(f AttrFilter, e *ElementData) bool {
	var actual string
	switch f.Attr {
	case "name":
		actual = e.Name
	case "value":
		actual = e.Value
	case "description":
		actual = e.Description
	case "role":
		actual = string(e.Role)
	default:
		if e.Raw != nil {
			if v, ok := e.Raw[f.Attr]; ok {
				actual = fmt.Sprint(v)
			}
		}
	}
	return matchOp(f.Op, f.Value, actual)
}

func matchOp(op MatchOp, expected, actual string) bool {
	switch op {
	case OpEqual:
		return actual == expected
	case OpContains:
		return strings.Contains(actual, expected)
	case OpStartsWith:
		return strings.HasPrefix(actual, expected)
	case OpEndsWith:
		return strings.HasSuffix(actual, expected)
	case OpNotEqual:
		return actual != expected
	case OpNotContains:
		return !strings.Contains(actual, expected)
	case OpNotStartsWith:
		return !strings.HasPrefix(actual, expected)
	case OpNotEndsWith:
		return !strings.HasSuffix(actual, expected)
	}
	return false
}
