# uPlot (vendored)

- **Version**: 1.6.32
- **Source**: https://registry.npmjs.org/uplot/-/uplot-1.6.32.tgz
- **Upstream registry metadata**: https://registry.npmjs.org/uplot/1.6.32
- **License**: MIT (Copyright (c) 2022 Leon Sorokin) — see `LICENSE` in this directory.

## Integrity

Verified against the npm registry's `dist.shasum` / `dist.integrity` for
`uplot@1.6.32` before vendoring:

- sha1 (shasum): `c800a63b432bad692d6d746f44f0882aa73a49ae`
- sha512 (integrity): `sha512-KIMVnG68zvu5XXUbC4LQEPnhwOxBuLyW1AHtpm6IKTXImkbLgkMy+jabjLgSLMasNuGGzQm/ep3tOkyTxpiQIw==`

Both hashes were recomputed locally against the downloaded tarball
(`sha1sum`, and a Node `crypto.createHash('sha512')` digest of the tarball
bytes) and matched the registry values exactly before any file was copied
out of the package.

## Files

- `uPlot.esm.js` — from `package/dist/uPlot.esm.js` in the npm tarball, used
  as-is via a native `<script type="module">` import. No bundler involved.
- `uPlot.min.css` — from `package/dist/uPlot.min.css`.
- `LICENSE` — from `package/LICENSE`.

## Why vendored, not CDN

The hub's CSP (`script-src 'self'`, no runtime CDN allowed per
`CODING_CONVENTIONS.md` "Embedded frontend standards") requires every
third-party asset to be served from the same origin as the hub, embedded in
the Go binary via `//go:embed`. Nothing here is modified from upstream.
