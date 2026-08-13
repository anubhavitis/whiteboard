#!/usr/bin/env python3
"""Generate -light and -dark variants of each base diagram in docs/.

Why two files rather than one clever one: GitHub renders `<img src="*.svg">`
against a white background of its own, and does not apply `prefers-color-scheme`
inside the SVG. So a single transparent file tuned for dark mode shows as a white
slab on github.com no matter what it contains. Two files selected by a `<picture>`
element is the only mechanism that actually works there.

The base `<name>.svg` files are the source of truth — edit those, then run:

    python3 docs/make-theme-variants.py

Variants are committed rather than built on the fly, because GitHub serves files
from the repo and cannot run a build step.
"""

import pathlib

BASES = ["turn-flow", "agent-seam", "tool-loop", "skills"]

# The base files use a mid-range palette that survives both themes. Each variant
# pushes it toward one end for real contrast.
LIGHT = [
    ('fill="#9aa7ba"', 'fill="#1f2937"'),    # node labels -> near-black
    ('fill="#8899ad"', 'fill="#475569"'),    # captions, container titles
    ('fill="#7d8ba1"', 'fill="#475569"'),    # edge labels
    ('fill="#94a3b8"', 'fill="#64748b"'),
    ('stroke="#8899ad"', 'stroke="#64748b"'),
    ('stroke="#7c8cf8"', 'stroke="#6366f1"'),
    ('stroke="#8b5cf6"', 'stroke="#7c3aed"'),
    ('fill="#8b5cf6"', 'fill="#6d28d9"'),
    ('fill="#e0796f"', 'fill="#b91c1c"'),
    ('stroke="#e0796f"', 'stroke="#dc2626"'),
]

LIGHT_MARKERS = [
    ('<path d="M0,0 L10,5 L0,10 z" fill="#8899ad"/>', '<path d="M0,0 L10,5 L0,10 z" fill="#64748b"/>'),
    ('<path d="M0,0 L10,5 L0,10 z" fill="#8b5cf6"/>', '<path d="M0,0 L10,5 L0,10 z" fill="#7c3aed"/>'),
    ('<path d="M0,0 L10,5 L0,10 z" fill="#e0796f"/>', '<path d="M0,0 L10,5 L0,10 z" fill="#dc2626"/>'),
]

DARK = [
    ('fill="#9aa7ba"', 'fill="#c9d3e0"'),    # node labels -> brighter
    ('fill="#7d8ba1"', 'fill="#93a1b5"'),
]


def write_variant(src: str, replacements, out: pathlib.Path) -> None:
    body = src
    for old, new in replacements:
        body = body.replace(old, new)
    out.write_text(body)


def main() -> None:
    docs = pathlib.Path(__file__).parent
    for base in BASES:
        src = (docs / f"{base}.svg").read_text()
        write_variant(src, LIGHT + LIGHT_MARKERS, docs / f"{base}-light.svg")
        write_variant(src, DARK, docs / f"{base}-dark.svg")
        print(f"{base}: wrote -light and -dark")


if __name__ == "__main__":
    main()
