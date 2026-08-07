# Cytoscape.js

- Version: 3.34.0
- Upstream: <https://github.com/cytoscape/cytoscape.js/tree/v3.34.0>
- Embedded asset: `internal/web/static/cytoscape-3.34.0.min.js`
- SHA-256: `9c2a3bf2592e0b14a1f7bec07c03a54f16dedf32af9cd0af155c716aa6c87bc3`
- License: MIT (`LICENSE` in this directory)

The UMD build, which sets `window.cytoscape` and contains no `eval` or
`new Function`, so it runs under the served Content-Security-Policy
without `script-src 'unsafe-eval'`.

The asset is served from the pgConsole binary. Runtime CDN access is not
used. `TestVendoredCytoscapeIsPinned` makes replacements explicit.
