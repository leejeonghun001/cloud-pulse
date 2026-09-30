package billing

import "context"

// fakeCall records one CommandRunner.Run invocation seen by fakeRunner.
type fakeCall struct {
	Dir  string
	Env  []string
	Name string
	Args []string
}

// fakeRunner is a deterministic CommandRunner for tests: each call to
// Run consumes the next entry of Results (by argv match against Rules,
// falling back to Default if no rule matches), recording every call it
// receives for assertions.
type fakeRunner struct {
	// Rules maps a joined "name arg1 arg2 ..." prefix to the Result to
	// return when a call's argv starts with that prefix. The longest
	// matching rule key wins so callers can special-case one specific
	// call (e.g. the forecast call) while a shorter prefix (e.g. just
	// "aws") supplies a default for everything else.
	Rules map[string]Result
	// Errs maps the same key space to a Go error to return instead of a
	// Result (simulating Run's own bookkeeping failing, not a CLI
	// exit).
	Errs map[string]error
	// Default is returned when no rule matches.
	Default Result

	Calls []fakeCall
}

func (f *fakeRunner) Run(_ context.Context, dir string, env []string, name string, args ...string) (Result, error) {
	f.Calls = append(f.Calls, fakeCall{Dir: dir, Env: env, Name: name, Args: append([]string{}, args...)})

	key := name
	for _, a := range args {
		key += " " + a
	}

	bestKey := ""
	for k := range f.Rules {
		if hasPrefixWords(key, k) && len(k) > len(bestKey) {
			bestKey = k
		}
	}
	for k := range f.Errs {
		if hasPrefixWords(key, k) && len(k) > len(bestKey) {
			bestKey = k
		}
	}
	if bestKey == "" {
		return f.Default, nil
	}
	if err, ok := f.Errs[bestKey]; ok {
		return Result{}, err
	}
	return f.Rules[bestKey], nil
}

// hasPrefixWords reports whether key starts with prefix at a word
// boundary (prefix itself, or prefix followed by a space).
func hasPrefixWords(key, prefix string) bool {
	if key == prefix {
		return true
	}
	if len(key) > len(prefix) && key[:len(prefix)] == prefix && key[len(prefix)] == ' ' {
		return true
	}
	return false
}
