# Inter (vendored web font)

- **Version**: 5.2.8
- **Source**: https://registry.npmjs.org/@fontsource-variable/inter/-/inter-5.2.8.tgz
- **Upstream registry metadata**: https://registry.npmjs.org/@fontsource-variable/inter/5.2.8
- **Upstream project**: https://github.com/rsms/inter
- **License**: OFL-1.1 (SIL Open Font License 1.1) — see `LICENSE` in this directory.

## What is vendored

Only `files/inter-latin-wght-normal.woff2` from the package, copied unmodified as
`inter-latin-wght-normal.woff2` (variable weight axis, Latin subset, 48,256 bytes,
SHA-256 `3100e775e8616cd2611beecfa23a4263d7037586789b43f035236a2e6fbd4c62`). The dashboard self-hosts it — no font CDN, no third-party request.
Hangul and other scripts fall back to the system font stack declared in `web/src/tokens.css`.

## Integrity

Verified against the npm registry's `dist.shasum` / `dist.integrity` for
`@fontsource-variable/inter@5.2.8` before vendoring:

- sha1 (shasum): `29b11476f5149f6a443b4df6516e26002d87941a`
- sha512 (integrity): `sha512-kOfP2D+ykbcX/P3IFnokOhVRNoTozo5/JxhAIVYLpea/UBmCQ/YWPBfWIDuBImXX/15KH+eKh4xpEUyS2sQQGQ==`

Both hashes were recomputed locally against the downloaded tarball (`sha1sum` and
`openssl dgst -sha512`) and matched the registry values before any file was copied.
