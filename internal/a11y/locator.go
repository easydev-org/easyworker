package goa11y

import (
	"strings"
	"time"
)

// Locator finds elements matching a selector within a scope (a provider plus an
// optional root element), with optional auto-waiting.
type Locator struct {
	provider Provider
	root     *ElementData // nil = whole desktop
	selector string
	group    *SelectorGroup
	nth      int // 0 = all
}

// NewLocator creates a locator scoped to root (nil = desktop).
func NewLocator(p Provider, root *ElementData, selector string) *Locator {
	return &Locator{provider: p, root: root, selector: selector}
}

// Nth restricts the locator to the 1-based nth match.
func (l *Locator) Nth(n int) *Locator {
	c := *l
	c.nth = n
	return &c
}

// Elements returns all current matches (no waiting).
func (l *Locator) Elements() ([]*Element, error) {
	g, err := l.parse()
	if err != nil {
		return nil, err
	}
	var out []*Element
	seen := map[uint64]bool{}
	for _, sel := range g.Selectors {
		matches, err := l.runSelector(&sel)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if !seen[m.data.Handle] {
				seen[m.data.Handle] = true
				out = append(out, m)
			}
		}
	}
	if l.nth > 0 {
		if l.nth > len(out) {
			return nil, SelectorNotMatched(l.selector).Diagnose(&Diagnosis{
				LastObserved: "selector matched fewer elements than requested",
			})
		}
		return out[l.nth-1 : l.nth], nil
	}
	return out, nil
}

// Wait polls until at least one element matches, or the timeout elapses.
func (l *Locator) Wait(timeout time.Duration) (*Element, error) {
	deadline := time.Now().Add(timeout)
	var lastScope string
	for {
		els, err := l.Elements()
		if err == nil && len(els) > 0 {
			return els[0], nil
		}
		if timeout == 0 || time.Now().After(deadline) {
			if lastScope == "" && l.root != nil {
				lastScope, _ = dumpTree(l.provider, l.root, intPtr(6))
			}
			return nil, SelectorNotMatched(l.selector).Diagnose(&Diagnosis{
				Condition:    "selector match",
				LastObserved: "selector never matched",
				Scope:        lastScope,
			})
		}
		time.Sleep(LookupPollInterval)
	}
}

func (l *Locator) parse() (*SelectorGroup, error) {
	if l.group == nil {
		g, err := ParseSelectorGroup(l.selector)
		if err != nil {
			return nil, err
		}
		l.group = g
	}
	return l.group, nil
}

// runSelector resolves one selector clause starting from the scope root.
func (l *Locator) runSelector(sel *Selector) ([]*Element, error) {
	// Determine the candidate roots.
	var roots []ElementData
	if l.root != nil {
		roots = []ElementData{*l.root}
	} else {
		apps, err := l.provider.ListApps()
		if err != nil {
			return nil, err
		}
		roots = apps
	}

	// Start with the first segment: match among descendants (or roots).
	current, err := l.matchSegment(roots, &sel.Segments[0].Simple, sel.Segments[0].Combinator == CombinatorRoot)
	if err != nil {
		return nil, err
	}
	// Walk remaining segments.
	for i := 1; i < len(sel.Segments); i++ {
		seg := sel.Segments[i]
		var next []ElementData
		for _, base := range current {
			var cands []ElementData
			switch seg.Combinator {
			case CombinatorChild:
				kids, err := l.provider.GetChildren(&base)
				if err != nil {
					return nil, err
				}
				cands = kids
			case CombinatorDescendant:
				cands = l.allDescendants(&base)
			}
			for j := range cands {
				if MatchesSimple(&cands[j], &seg.Simple) {
					next = append(next, cands[j])
				}
			}
		}
		current = dedupeData(next)
	}
	out := make([]*Element, len(current))
	for i := range current {
		out[i] = NewElement(current[i], l.provider)
	}
	return out, nil
}

// matchSegment matches the first segment. When includeRoot is true and a root
// is set, the root itself is considered; otherwise only descendants.
func (l *Locator) matchSegment(roots []ElementData, s *SimpleSelector, includeRoot bool) ([]ElementData, error) {
	var out []ElementData
	for i := range roots {
		if l.root != nil {
			// scoped: search within the root subtree
			if includeRoot && MatchesSimple(&roots[i], s) {
				out = append(out, roots[i])
			}
			for _, d := range l.allDescendants(&roots[i]) {
				if MatchesSimple(&d, s) {
					out = append(out, d)
				}
			}
		} else {
			// desktop scope: the app roots plus their subtrees
			if MatchesSimple(&roots[i], s) {
				out = append(out, roots[i])
			}
			for _, d := range l.allDescendants(&roots[i]) {
				if MatchesSimple(&d, s) {
					out = append(out, d)
				}
			}
		}
	}
	return dedupeData(out), nil
}

func (l *Locator) allDescendants(root *ElementData) []ElementData {
	var out []ElementData
	var walk func(d *ElementData, depth int)
	walk = func(d *ElementData, depth int) {
		if depth > MaxTreeDepth {
			return
		}
		kids, err := l.provider.GetChildren(d)
		if err != nil {
			return
		}
		for i := range kids {
			out = append(out, kids[i])
			walk(&kids[i], depth+1)
		}
	}
	walk(root, 0)
	return out
}

func dedupeData(in []ElementData) []ElementData {
	seen := map[uint64]bool{}
	out := in[:0]
	for i := range in {
		if !seen[in[i].Handle] {
			seen[in[i].Handle] = true
			out = append(out, in[i])
		}
	}
	return out
}

func intPtr(n int) *int { return &n }

// First polls for the first match (convenience over Wait with the default timeout).
func (l *Locator) First() (*Element, error) {
	return l.Wait(defaultTimeout)
}

// Count returns the number of matches.
func (l *Locator) Count() (int, error) {
	els, err := l.Elements()
	return len(els), err
}

// WaitText polls until an element's value/name contains text.
func (l *Locator) WaitText(timeout time.Duration, text string) (*Element, error) {
	deadline := time.Now().Add(timeout)
	for {
		els, err := l.Elements()
		if err == nil {
			for _, e := range els {
				if strings.Contains(e.data.Name, text) || strings.Contains(e.data.Value, text) {
					return e, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, SelectorNotMatched(l.selector).Diagnose(&Diagnosis{
				Condition:    "text contains " + text,
				LastObserved: "not observed",
			})
		}
		time.Sleep(LookupPollInterval)
	}
}
