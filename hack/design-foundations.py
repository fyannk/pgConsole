#!/usr/bin/env python3
"""Generate the design system's foundations page from the real console.

The foundations card used to be hand-authored with a frozen copy of the
stylesheet pasted into it. It drifted the moment the console changed, and
because it *looked* like a stylesheet reference nobody noticed it was a
snapshot of one taken months earlier.

So it is generated. The tokens are parsed out of the real app.css, and
every component sample is lifted from a page the bundle just captured
from the running console, which means a component that changes shape
changes here too and one that disappears cannot linger.

Usage: design-foundations.py <app.css> <pages-dir> <out-file>
"""

import html
import re
import sys
from html.parser import HTMLParser


class Extractor(HTMLParser):
    """Collects the outer HTML of the first element matching a selector."""

    def __init__(self, tag, cls):
        super().__init__(convert_charrefs=False)
        self.want_tag = tag
        self.want_cls = cls
        self.depth = 0
        self.parts = []
        self.done = False
        # Void elements never nest, so they must not push the depth.
        self.void = {"area", "base", "br", "col", "embed", "hr", "img",
                     "input", "link", "meta", "param", "source", "track", "wbr"}

    def _matches(self, tag, attrs):
        if tag != self.want_tag:
            return False
        classes = dict(attrs).get("class") or ""
        return self.want_cls in classes.split()

    def handle_starttag(self, tag, attrs):
        if self.done:
            return
        if not self.depth and self._matches(tag, attrs):
            self.depth = 1
            self.parts.append(self.get_starttag_text())
            return
        if self.depth:
            self.parts.append(self.get_starttag_text())
            if tag not in self.void:
                self.depth += 1

    def handle_startendtag(self, tag, attrs):
        if self.depth and not self.done:
            self.parts.append(self.get_starttag_text())

    def handle_endtag(self, tag):
        if not self.depth or self.done:
            return
        if tag in self.void:
            return
        self.depth -= 1
        self.parts.append(f"</{tag}>")
        if self.depth == 0:
            self.done = True

    def handle_data(self, data):
        if self.depth and not self.done:
            self.parts.append(data)

    def handle_entityref(self, name):
        if self.depth and not self.done:
            self.parts.append(f"&{name};")

    def handle_charref(self, name):
        if self.depth and not self.done:
            self.parts.append(f"&#{name};")

    def handle_comment(self, data):
        if self.depth and not self.done:
            self.parts.append(f"<!--{data}-->")

    def result(self):
        return "".join(self.parts) if self.done else ""


def sample(pages_dir, page, tag, cls):
    """Lift one component out of a captured page."""
    try:
        with open(f"{pages_dir}/{page}", encoding="utf-8") as fh:
            source = fh.read()
    except OSError:
        return ""
    extractor = Extractor(tag, cls)
    extractor.feed(source)
    return extractor.result()


def tokens(css, block):
    """Parse the custom properties of one :root block.

    Comments are stripped first: several tokens are declared right after
    a paragraph explaining why they exist, and a naive value match drags
    that prose into the table.
    """
    css = re.sub(r"/\*.*?\*/", "", css, flags=re.S)
    match = re.search(block + r"\s*\{(.*?)\n\}", css, re.S)
    if not match:
        return {}
    found = {}
    for name, value in re.findall(r"(--[\w-]+):\s*([^;]+);", match.group(1)):
        found[name] = " ".join(value.split())
    return found


def swatches(light, dark, names, kind):
    rows = []
    for name in names:
        if name not in light:
            continue
        chip = ""
        if kind == "color":
            chip = (f'<span class="fnd-chip" style="background: var({name})"></span>')
        rows.append(
            "<tr>"
            f"<td>{chip}<code>{html.escape(name)}</code></td>"
            f"<td>{html.escape(light[name])}</td>"
            f"<td>{html.escape(dark.get(name, '—'))}</td>"
            "</tr>"
        )
    return "\n".join(rows)


def section(title, meta, body):
    if not body.strip():
        return ""
    return f"""
    <section class="panel sectioned">
      <div class="panel-head"><h2>{title}</h2><p class="meta">{meta}</p></div>
      <div class="panel-body">
{body}
      </div>
    </section>"""


def main():
    css_path, pages_dir, out_path = sys.argv[1], sys.argv[2], sys.argv[3]
    with open(css_path, encoding="utf-8") as fh:
        css = fh.read()

    light = tokens(css, r":root")
    dark = tokens(css, r"@media \(prefers-color-scheme: dark\)\s*\{\s*:root")

    colour_names = [n for n in light if n in (
        "--bg", "--surface", "--surface-2", "--text", "--text-muted",
        "--text-faint", "--border", "--border-strong", "--accent",
        "--accent-weak", "--ok", "--warn", "--warn-weak", "--bad",
        "--bad-weak", "--unknown")]
    type_names = [n for n in light if n.startswith("--text-") and n[7:].replace("-", "").isalnum()
                  and n not in ("--text-muted", "--text-faint")] + ["--font-ui", "--font-mono"]
    space_names = [n for n in light if n.startswith("--space-")] + \
        [n for n in ("--radius", "--radius-sm", "--measure", "--header-h") if n in light]

    parts = [
        section(
            "Colour",
            "parsed from the stylesheet, light and dark",
            '<div class="table-scroll"><table><caption>Surface, text and status tokens</caption>'
            "<thead><tr><th>Token</th><th>Light</th><th>Dark</th></tr></thead><tbody>"
            + swatches(light, dark, colour_names, "color")
            + "</tbody></table></div>"
            '<p class="meta">Every status hue is paired with a word and a shape mark in the markup, '
            "so removing colour removes nothing.</p>",
        ),
        section(
            "Type",
            "two stacks, split by meaning",
            '<div class="table-scroll"><table><caption>Type tokens</caption>'
            "<thead><tr><th>Token</th><th>Light</th><th>Dark</th></tr></thead><tbody>"
            + swatches(light, dark, sorted(set(type_names)), "text")
            + "</tbody></table></div>"
            '<p class="meta">The Content-Security-Policy declares no <code>font-src</code>, so no web font '
            "can load. Chrome is the UI sans; machine-reported data is monospace everywhere it appears.</p>",
        ),
        section(
            "Spacing and shape",
            "the scale everything is built on",
            '<div class="table-scroll"><table><caption>Spacing, radius and measure</caption>'
            "<thead><tr><th>Token</th><th>Light</th><th>Dark</th></tr></thead><tbody>"
            + swatches(light, dark, space_names, "text")
            + "</tbody></table></div>",
        ),
        section("Top bar", "fixed; carries the target and the refresh control",
                sample(pages_dir, "index-healthy.html", "header", "topbar")),
        section("Sidebar", "the section map, with inert entries for what this build does not serve",
                sample(pages_dir, "index-baseline.html", "aside", "sidebar")),
        section("Verdict", "the plain-language answer the Overview opens with",
                sample(pages_dir, "index-healthy.html", "div", "verdict")),
        section("Cards", "derived summary values, each naming its origin",
                sample(pages_dir, "index-healthy.html", "div", "cards")),
        section("Wiring diagram", "server-drawn geometry, re-laid out by the enhancement layer",
                sample(pages_dir, "index-healthy.html", "section", "topo-panel")),
        section("Status board", "the verdict and the four identifying facts",
                sample(pages_dir, "cluster-status.html", "div", "statusboard")),
        section("Instance cards", "the primary carries the only emphasis",
                sample(pages_dir, "cluster-status.html", "div", "topology")),
        section("Fact list", "term and value, monospace on the value",
                sample(pages_dir, "cluster-status.html", "dl", "facts")),
        section("Table", "the primary surface of a data console",
                sample(pages_dir, "databases-roles.html", "div", "table-scroll")),
        section("Empty state", "observed, and there is nothing — a different claim from unobserved",
                sample(pages_dir, "databases-empty.html", "div", "empty")),
        section("Finding", "a disagreement between two sources, never resolved silently",
                sample(pages_dir, "cluster-pods-empty.html", "div", "finding")),
        section("Log output", "bounded, on demand, never stored",
                sample(pages_dir, "logs-tail.html", "pre", "logs")),
    ]

    body = "\n".join(p for p in parts if p)
    out = f"""<!-- @dsCard group="Foundations" name="Foundations" subtitle="Tokens parsed from the stylesheet and components lifted from the captured pages" -->
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>pgConsole — foundations</title>
  <link rel="stylesheet" href="../pages/console.css">
  <style>
    /* Gallery framing only. Every component below is unstyled here and
       inherits entirely from console.css, so it renders exactly as the
       console renders it. */
    body {{ margin: 0 auto; max-width: 68rem; padding: var(--space-6) var(--space-5) var(--space-7); }}
    main {{ display: flex; flex-direction: column; gap: var(--space-4); }}
    .fnd-chip {{
      border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
      display: inline-block; height: 1em; margin-right: var(--space-2);
      vertical-align: -0.15em; width: 1.6em;
    }}
    /* The samples are lifted whole, and two of them are fixed or sticky
       in the console. Neutralise that here so they sit in the flow. */
    .panel-body header.topbar {{ position: static; }}
    .panel-body aside.sidebar {{ max-height: none; position: static; }}
  </style>
</head>
<body>
  <header>
    <h1>pgConsole — foundations</h1>
    <dl class="target">
      <dt>Stylesheet</dt><dd>internal/web/static/app.css</dd>
      <dt>Web fonts</dt><dd>none — no font-src in the CSP</dd>
      <dt>Samples</dt><dd data-state="current">lifted from the captured pages</dd>
    </dl>
  </header>
  <main>{body}
  </main>
</body>
</html>
"""
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write(out)
    print(f"[design]   foundations/foundations.html ({len([p for p in parts if p])} sections)")


if __name__ == "__main__":
    main()
