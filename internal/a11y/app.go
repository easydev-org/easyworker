package goa11y

import "time"

// LookupPollInterval is how often App lookups retry.
const LookupPollInterval = 100 * time.Millisecond

// DefaultTimeout is the default wait for App/locator lookups.
var defaultTimeout = 5 * time.Second

// DefaultTimeout returns the process-wide default lookup timeout.
func DefaultTimeout() time.Duration { return defaultTimeout }

// SetDefaultTimeout overrides the default lookup timeout.
func SetDefaultTimeout(d time.Duration) { defaultTimeout = d }

// App is a live handle to a top-level application bound to its provider.
type App struct {
	element  *Element
	provider Provider
}

// AppByName polls for an application by display name.
func AppByName(p Provider, name string, timeout time.Duration) (*App, error) {
	return lookupApp(p, timeout, func(d *ElementData) bool { return d.Name == name })
}

// AppByPID resolves an application by process id.
func AppByPID(p Provider, pid uint32, timeout time.Duration) (*App, error) {
	deadline := time.Now().Add(timeout)
	for {
		d, err := p.AppByPID(pid)
		if err == nil && d != nil {
			return &App{element: NewElement(*d, p), provider: p}, nil
		}
		if time.Now().After(deadline) {
			return nil, SelectorNotMatched("pid=" + itoa(int(pid))).Diagnose(&Diagnosis{
				Condition:    "application with pid",
				LastObserved: "not found",
			})
		}
		time.Sleep(LookupPollInterval)
	}
}

func lookupApp(p Provider, timeout time.Duration, match func(*ElementData) bool) (*App, error) {
	deadline := time.Now().Add(timeout)
	var lastCandidates []string
	for {
		apps, err := p.ListApps()
		if err == nil {
			lastCandidates = lastCandidates[:0]
			for i := range apps {
				if match(&apps[i]) {
					return &App{element: NewElement(apps[i], p), provider: p}, nil
				}
				lastCandidates = append(lastCandidates, string(apps[i].Role)+" \""+apps[i].Name+"\"")
			}
		}
		if time.Now().After(deadline) {
			return nil, SelectorNotMatched("application").Diagnose(&Diagnosis{
				Condition:    "application by name",
				LastObserved: "selector never matched",
				Candidates:   bound(lastCandidates, 20),
			})
		}
		time.Sleep(LookupPollInterval)
	}
}

func (a *App) Element() *Element   { return a.element }
func (a *App) Name() string        { return a.element.data.Name }
func (a *App) PID() (uint32, bool) { return a.element.PID() }

func (a *App) Windows() ([]*Element, error) {
	ds, err := a.provider.AppWindows(&a.element.data)
	if err != nil {
		return nil, err
	}
	return wrap(a.provider, ds), nil
}

func (a *App) Dump(maxDepth *int) (string, error) {
	return dumpTree(a.provider, &a.element.data, maxDepth)
}

// Locator returns a locator scoped to this application.
func (a *App) Locator(selector string) *Locator {
	return NewLocator(a.provider, &a.element.data, selector)
}

func bound[T any](in []T, n int) []T {
	if len(in) > n {
		return in[:n]
	}
	return in
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
