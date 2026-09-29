# Lucide icons (vendored path data)

- **Source**: https://github.com/lucide-icons/lucide (main branch icon
  SVGs, `icons/<name>.svg`), fetched individually per icon.
- **License**: ISC — see `LICENSE` in this directory.
- **What's vendored**: only the raw SVG child-element data (`<path>`,
  `<circle>`, `<line>`, `<rect>`, `<polyline>` tags and their
  attributes) for the small subset of icons this dashboard uses, hand
  -transcribed into `web/assets/js/ui/icons.js` as plain JS data
  objects. No Lucide JS/React/build tooling is vendored — icons are
  rendered by a tiny local helper (`createElementNS`) reading this data,
  keeping the dependency footprint to icon path data only.
- **Not modified** from upstream path data. The outer `<svg>` wrapper
  attributes (`width="24" height="24" viewBox="0 0 24 24" fill="none"
  stroke="currentColor" stroke-width="2" stroke-linecap="round"
  stroke-linejoin="round"`) are reproduced identically for every icon by
  `icons.js`'s shared builder rather than repeated per icon.
- **Why vendored, not CDN**: the hub's CSP (`script-src 'self'`, no
  runtime CDN) requires every asset to be served from the same origin,
  embedded in the Go binary via `//go:embed`, per
  `CODING_CONVENTIONS.md`'s "Embedded frontend standards".
