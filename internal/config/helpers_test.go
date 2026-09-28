package config

// mapLookup adapts a map[string]string into a LookupFunc, matching
// os.LookupEnv semantics. Other packages' tests reuse this helper.
func mapLookup(m map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}
