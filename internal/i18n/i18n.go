// Package i18n provides server-side localization for hub-generated
// notification message bodies and chart titles (SPEC-v0.8 §3-3's
// "notify_language" hub setting). It is intentionally separate from
// and has no dependency on the web dashboard's own JS i18n module
// (web/assets/js/core/i18n.js) — the two are localized independently,
// one for the browser UI, one for outbound notification text.
//
// This package does NOT translate existing alert/notification message
// strings itself; it only provides the Lang type, catalog lookup, and
// parity test infrastructure that a later stage wires into
// internal/alerting and internal/notify's message-building code.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed catalog/*.json
var catalogFS embed.FS

// Lang is a supported notification-message language code.
type Lang string

// Supported languages. English is always the fallback for a missing
// key or an unrecognized/empty Lang value.
const (
	LangEn Lang = "en"
	LangKo Lang = "ko"
)

// DefaultLang is used whenever no language has been configured (the
// "notify_language" hub setting is absent or empty).
const DefaultLang = LangEn

// Parse validates s as a Lang, returning DefaultLang and ok=false for
// an empty, unrecognized, or differently-cased value — callers that
// need strict validation (e.g. the settings API) should check ok
// themselves rather than relying on the fallback value.
func Parse(s string) (lang Lang, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(LangEn):
		return LangEn, true
	case string(LangKo):
		return LangKo, true
	default:
		return DefaultLang, false
	}
}

// catalog is a flat map from dot-separated key (e.g.
// "channel_health.failed") to its message template. Flattened once at
// package init from the embedded nested JSON so T's lookups are O(1)
// map accesses rather than a JSON-tree walk per call.
type catalog map[string]string

var catalogs map[Lang]catalog

func init() {
	catalogs = map[Lang]catalog{
		LangEn: mustLoadCatalog("en"),
		LangKo: mustLoadCatalog("ko"),
	}
}

func mustLoadCatalog(name string) catalog {
	raw, err := catalogFS.ReadFile("catalog/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("i18n: read embedded catalog %q: %v", name, err)) // allow-panic: embedded at build time; a read failure is a build/packaging bug, not a runtime condition
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		panic(fmt.Sprintf("i18n: parse embedded catalog %q: %v", name, err)) // allow-panic: catalog JSON is authored in this repo and validated by TestCatalogParity_*; a parse failure means the catalog itself is broken, not a runtime condition
	}
	flat := catalog{}
	flatten("", tree, flat)
	return flat
}

// flatten walks a nested map[string]any tree (as decoded from a
// namespaced catalog JSON file) into dot-separated keys in dst, e.g.
// {"channel_health":{"failed":"..."}} becomes {"channel_health.failed":
// "..."}. Non-string, non-object leaves are rejected at init time via
// a panic (a catalog authoring mistake, not a runtime condition).
func flatten(prefix string, node map[string]any, dst catalog) {
	for k, v := range node {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			dst[key] = val
		case map[string]any:
			flatten(key, val, dst)
		default:
			panic(fmt.Sprintf("i18n: catalog key %q has non-string, non-object value %T", key, v)) // allow-panic: catalog JSON is authored in this repo; an invalid leaf shape is an authoring bug caught at init, not a runtime condition
		}
	}
}

// T returns the message for key in lang, with {param} placeholders in
// the template replaced from params, falling back to English if the
// key is missing in lang, and to the bare key itself if missing from
// every catalog (mirroring the web dashboard's core/i18n.js t()
// fallback contract, so both layers behave predictably the same way
// when a translation is missing).
func T(lang Lang, key string, params map[string]string) string {
	tmpl, ok := catalogs[lang][key]
	if !ok {
		tmpl, ok = catalogs[LangEn][key]
	}
	if !ok {
		return key
	}
	return applyParams(tmpl, params)
}

func applyParams(tmpl string, params map[string]string) string {
	if len(params) == 0 {
		return tmpl
	}
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		if tmpl[i] == '{' {
			if end := strings.IndexByte(tmpl[i:], '}'); end > 0 {
				name := tmpl[i+1 : i+end]
				if val, ok := params[name]; ok {
					b.WriteString(val)
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String()
}

// Keys returns every key present in lang's catalog, sorted. Exported
// for the parity test and for other packages that need to validate a
// literal key exists before calling T with it.
func Keys(lang Lang) []string {
	out := make([]string, 0, len(catalogs[lang]))
	for k := range catalogs[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Placeholders extracts the sorted, de-duplicated set of {name}
// placeholders in a catalog's template for key, or nil if the key is
// absent from that catalog. Exported for the parity test, which
// checks that every key's placeholder set matches across languages.
func Placeholders(lang Lang, key string) []string {
	tmpl, ok := catalogs[lang][key]
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	i := 0
	for i < len(tmpl) {
		if tmpl[i] == '{' {
			if end := strings.IndexByte(tmpl[i:], '}'); end > 0 {
				name := tmpl[i+1 : i+end]
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
				i += end + 1
				continue
			}
		}
		i++
	}
	sort.Strings(out)
	return out
}
