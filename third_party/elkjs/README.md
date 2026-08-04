# ELK.js (elkjs)

- Version: 0.12.0
- Upstream: <https://github.com/kieler/elkjs/tree/v0.12.0>
- Embedded asset: `internal/web/static/elk-0.12.0.bundled.js`
- SHA-256: `1222e44f953ce7746af23801e723708f8e6f436b8b377a6a5fc7552f34a307b3`
- License: EPL-2.0 OR GPL-3.0-or-later (`LICENSE` in this directory)

The license is the one thing here that is unlike the other vendored
assets, which are all MIT. ELK is dual-licensed EPL-2.0 or
GPL-3.0-or-later; pgConsole takes it under the EPL, whose copyleft is
file-scoped — the file is redistributed unmodified, under its own
license, with this notice, and it imposes nothing on the Apache-2.0
sources around it. Modifying this file would put the modifications under
the EPL, so it is never patched: replacing it means taking a new upstream
release whole.

The bundled build, which sets `window.ELK` and contains no `eval` or
`new Function`, so it runs under the served Content-Security-Policy
without `script-src 'unsafe-eval'`. It touches `Worker` only when the
caller supplies `workerUrl` or `workerFactory`; the console supplies
neither, so the layout runs on the main thread and no `worker-src` is
needed against a `default-src 'none'` policy.

At 1.6 MB this is by far the largest embedded asset — larger than every
other vendored file put together.

The asset is served from the pgConsole binary. Runtime CDN access is not
used. `TestVendoredELKIsPinned` makes replacements explicit.
