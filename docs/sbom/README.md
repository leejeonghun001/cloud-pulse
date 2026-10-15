# Offline vendor SBOM

`vendor.cdx.json` is a deterministic CycloneDX 1.6 **source-bound inventory** of
the checked-in browser vendor files: uPlot 1.6.32 (MIT) and the unpinned Lucide
icon path-data subset (ISC), including exact SHA-256 hashes and file relationships.
The uPlot tarball digests are recorded only as historical claims in its vendoring
README; this generator does not independently verify the registry archive.
Lucide's version and upstream commit are unknown and intentionally omitted.

From the repository root, using only Python's standard library:

```sh
python3 scripts/generate-vendor-sbom.py
python3 scripts/generate-vendor-sbom.py --check
python3 scripts/tests/test_generate_vendor_sbom.py
```

Use `--output PATH` to write/check another location, and `--root PATH` to select
the source tree. `--check` refuses malformed, modified, stale, or non-canonical
artifacts. The included checks enforce this repository's narrow structural and
source-evidence profile; they are **not** full official CycloneDX schema
validation.

This is not an authoritative npm dependency graph, and excludes transitive,
build, and test dependencies. No vulnerability analysis is performed. It is not
a completeness or vulnerability-free certification.
