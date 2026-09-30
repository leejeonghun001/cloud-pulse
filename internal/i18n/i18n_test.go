package i18n

import (
	"reflect"
	"testing"
)

// TestParse covers every accepted spelling plus the fallback contract
// for invalid/empty input.
func TestParse(t *testing.T) {
	cases := []struct {
		in     string
		want   Lang
		wantOK bool
	}{
		{"en", LangEn, true},
		{"EN", LangEn, true},
		{" en ", LangEn, true},
		{"ko", LangKo, true},
		{"KO", LangKo, true},
		{"", DefaultLang, false},
		{"fr", DefaultLang, false},
		{"english", DefaultLang, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestT_KnownKey checks a real catalog key resolves with parameter
// substitution in both languages.
func TestT_KnownKey(t *testing.T) {
	got := T(LangEn, "channel_health.test_ok", map[string]string{"channel_name": "Ops Discord"})
	want := "Synthetic test to Ops Discord succeeded."
	if got != want {
		t.Errorf("T(en) = %q, want %q", got, want)
	}

	got = T(LangKo, "channel_health.test_ok", map[string]string{"channel_name": "Ops Discord"})
	want = "Ops Discord 채널로 보낸 연결 확인이 성공했습니다."
	if got != want {
		t.Errorf("T(ko) = %q, want %q", got, want)
	}
}

// TestT_FallsBackToEnglish simulates a key present only in English
// (as would happen mid-translation) by using a key that's actually
// only defined for one language isn't possible with the current
// parity-enforced catalogs, so this exercises the fallback path
// directly against an unrecognized language value routed through
// Parse first, matching how callers are expected to use this package.
func TestT_FallsBackToEnglish(t *testing.T) {
	lang, ok := Parse("fr")
	if ok {
		t.Fatalf("Parse(\"fr\") unexpectedly ok")
	}
	got := T(lang, "channel_health.test_ok", map[string]string{"channel_name": "X"})
	want := "Synthetic test to X succeeded."
	if got != want {
		t.Errorf("T(fallback) = %q, want %q", got, want)
	}
}

// TestT_MissingKeyReturnsKeyItself covers the last-resort fallback:
// a key absent from every catalog returns the key unchanged.
func TestT_MissingKeyReturnsKeyItself(t *testing.T) {
	got := T(LangEn, "does.not.exist", nil)
	if got != "does.not.exist" {
		t.Errorf("T(missing) = %q, want the key itself", got)
	}
}

// TestT_NoParamsLeavesPlaceholdersUnexpanded documents that omitting
// params (nil map) is a no-op substitution, not an error — useful for
// keys with no placeholders, and intentionally not "helpful" about
// keys that do have them (callers own building the correct params
// map; this package doesn't validate arity).
func TestT_NoParamsLeavesPlaceholdersUnexpanded(t *testing.T) {
	got := T(LangEn, "common.hub_name", nil)
	if got != "cloud-pulse" {
		t.Errorf("T(no params, no placeholders) = %q, want %q", got, "cloud-pulse")
	}

	got = T(LangEn, "channel_health.test_ok", nil)
	want := "Synthetic test to {channel_name} succeeded."
	if got != want {
		t.Errorf("T(no params, has placeholders) = %q, want unexpanded template %q", got, want)
	}
}

// TestCatalogParity_KeysMatch is the mandatory parity test (SPEC-v0.8
// §3-3 / Part B rules): en and ko must define exactly the same set of
// keys. Run this after every catalog edit — it is the single source of
// truth for "did I forget to translate a new key".
func TestCatalogParity_KeysMatch(t *testing.T) {
	enKeys := Keys(LangEn)
	koKeys := Keys(LangKo)

	enSet := map[string]bool{}
	for _, k := range enKeys {
		enSet[k] = true
	}
	koSet := map[string]bool{}
	for _, k := range koKeys {
		koSet[k] = true
	}

	for _, k := range enKeys {
		if !koSet[k] {
			t.Errorf("key %q present in en.json but missing from ko.json", k)
		}
	}
	for _, k := range koKeys {
		if !enSet[k] {
			t.Errorf("key %q present in ko.json but missing from en.json", k)
		}
	}
}

// TestCatalogParity_PlaceholdersMatch checks that every key's set of
// {placeholder} names is identical between en and ko, so a translator
// can never accidentally drop or rename a substitution variable.
func TestCatalogParity_PlaceholdersMatch(t *testing.T) {
	for _, key := range Keys(LangEn) {
		enPH := Placeholders(LangEn, key)
		koPH := Placeholders(LangKo, key)
		if !reflect.DeepEqual(enPH, koPH) {
			t.Errorf("key %q: en placeholders %v != ko placeholders %v", key, enPH, koPH)
		}
	}
}

// TestKeys_Sorted documents Keys' sorted-output contract, relied on by
// the parity tests above for deterministic diffs on failure.
func TestKeys_Sorted(t *testing.T) {
	keys := Keys(LangEn)
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("Keys() not sorted: %q >= %q", keys[i-1], keys[i])
		}
	}
}
