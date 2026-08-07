# Alpine.js (CSP build)

- Version: 3.15.12
- Upstream: <https://github.com/alpinejs/alpine/tree/v3.15.12>
- Embedded asset: `internal/web/static/alpine.csp.js`
- SHA-256: `42cdf3296d37730538765be5f4e5099fd9a43d3a9f242eae29dbdfa6dcc0223f`
- License: MIT (`LICENSE` in this directory)

Vendored from npm `@alpinejs/csp@3.15.12` (`dist/cdn.min.js`). The CSP build
evaluates no expression strings, so the console runs under a `script-src`
without `unsafe-eval`; the ordinary build does not.
`TestVendoredAlpineIsTheCSPBuild` makes replacements explicit.
