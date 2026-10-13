package goa11y

import (
	"fmt"
	"strings"
	"time"
)

// Diagnosis carries structured context on failure-path errors (timeouts,
// selector-not-matched). Mirrors xa11y's Diagnosis.
type Diagnosis struct {
	Condition    string
	Selector     string
	LastObserved string
	Candidates   []string
	Scope        string
}

func (d *Diagnosis) IsEmpty() bool {
	return d == nil || (d.Condition == "" && d.Selector == "" &&
		d.LastObserved == "" && len(d.Candidates) == 0 && d.Scope == "")
}

func (d *Diagnosis) String() string {
	if d.IsEmpty() {
		return ""
	}
	var b strings.Builder
	if d.Condition != "" {
		fmt.Fprintf(&b, "; waiting for: %s", d.Condition)
	}
	if d.Selector != "" {
		fmt.Fprintf(&b, "; selector: %s", d.Selector)
	}
	if d.LastObserved != "" {
		fmt.Fprintf(&b, "; last observed: %s", d.LastObserved)
	}
	if len(d.Candidates) > 0 {
		fmt.Fprintf(&b, "; candidates: %s", strings.Join(d.Candidates, ", "))
	}
	if d.Scope != "" {
		fmt.Fprintf(&b, "\nsearch scope (bounded):\n%s", d.Scope)
	}
	return b.String()
}

// ErrorKind discriminates Error variants.
type ErrorKind int

const (
	ErrPermissionDenied ErrorKind = iota
	ErrAccessibilityNotEnabled
	ErrSelectorNotMatched
	ErrElementStale
	ErrActionNotSupported
	ErrTextValueNotSupported
	ErrTimeout
	ErrInvalidSelector
	ErrInvalidActionData
	ErrInvalidConfig
	ErrNoElementBounds
	ErrUnsupported
	ErrPlatform
)

// Error is the structured error type, informative across FFI boundaries.
type Error struct {
	Kind ErrorKind
	// PermissionDenied / AccessibilityNotEnabled / Unsupported
	Instructions string
	App          string
	// SelectorNotMatched / ElementStale / InvalidSelector
	Selector string
	// ActionNotSupported
	Action string
	Role   Role
	// Timeout
	Elapsed time.Duration
	// InvalidSelector / InvalidActionData / InvalidConfig / Platform
	Message string
	// Platform
	Code int64
	// SelectorNotMatched / Timeout
	Diagnosis *Diagnosis
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrPermissionDenied:
		return fmt.Sprintf("Permission denied: %s", e.Instructions)
	case ErrAccessibilityNotEnabled:
		return fmt.Sprintf("Accessibility not enabled for %s: %s", e.App, e.Instructions)
	case ErrSelectorNotMatched:
		return fmt.Sprintf("No element matched selector: %s%s", e.Selector, e.Diagnosis.String())
	case ErrElementStale:
		return fmt.Sprintf("Element stale: could not relocate element for selector: %s", e.Selector)
	case ErrActionNotSupported:
		return fmt.Sprintf("Action %s not supported on %s", e.Action, e.Role)
	case ErrTextValueNotSupported:
		return "Text value input not supported for this element"
	case ErrTimeout:
		return fmt.Sprintf("Timeout after %.1fms%s", float64(e.Elapsed)/float64(time.Millisecond), e.Diagnosis.String())
	case ErrInvalidSelector:
		return fmt.Sprintf("Invalid selector '%s': %s", e.Selector, e.Message)
	case ErrInvalidActionData:
		return fmt.Sprintf("Invalid action data: %s", e.Message)
	case ErrInvalidConfig:
		return fmt.Sprintf("Invalid configuration: %s", e.Message)
	case ErrNoElementBounds:
		return "Element has no bounds"
	case ErrUnsupported:
		return fmt.Sprintf("Unsupported: %s", e.Instructions)
	case ErrPlatform:
		return fmt.Sprintf("Platform error (%d): %s", e.Code, e.Message)
	}
	return "unknown error"
}

func SelectorNotMatched(selector string) *Error {
	return &Error{Kind: ErrSelectorNotMatched, Selector: selector}
}

func Timeout(elapsed time.Duration) *Error {
	return &Error{Kind: ErrTimeout, Elapsed: elapsed}
}

// Diagnose attaches (or replaces) a Diagnosis on errors that carry one.
func (e *Error) Diagnose(d *Diagnosis) *Error {
	if e.Kind == ErrSelectorNotMatched || e.Kind == ErrTimeout {
		if d.IsEmpty() {
			e.Diagnosis = nil
		} else {
			e.Diagnosis = d
		}
	}
	return e
}

func Unsupported(feature string) *Error {
	return &Error{Kind: ErrUnsupported, Instructions: feature}
}

func PlatformErr(code int64, msg string) *Error {
	return &Error{Kind: ErrPlatform, Code: code, Message: msg}
}
