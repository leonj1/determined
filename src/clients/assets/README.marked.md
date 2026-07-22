# marked browser asset

`marked.min.js` is vendored from the published `marked@16.4.2` npm package,
extracted from `package/lib/marked.umd.js` inside
`https://registry.npmjs.org/marked/-/marked-16.4.2.tgz`. The project is
distributed under the MIT license in `LICENSE.marked.md`.

The UMD browser bundle is included as-is (the published build is already
minified). The two leading banner comments were stripped so the file begins at
the UMD wrapper; the code body is byte-identical to the upstream build. The
wrapper attaches `marked` to `globalThis` when loaded via a plain `<script>`
tag, exposing `globalThis.marked.Marked` for per-call instances.
