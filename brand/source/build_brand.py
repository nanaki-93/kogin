"""Build the Kasuri brand kit: logo SVGs, app icon SVG, color tokens.

PNG/ICNS/ICO rendering happens in render_brand.py (needs a browser).
"""
import json
import os
import sys

from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

FONT = sys.argv[1]
OUT = sys.argv[2]

# ---- palette -------------------------------------------------------------
DARK = {
    "emerald": "#34D399",
    "violet": "#9B7BFF",
    "text": "#EEEAF8",
    "tile": "#1B1433",
}
LIGHT = {
    "emerald": "#14955C",
    "violet": "#4B2FB8",
    "text": "#160F2B",
}

VX = [22, 44, 66]
HY = [22, 44, 66]
LO, HI = 14, 86          # thread ends; the mark's content box is LO..HI
SPAN = HI - LO
CROSS = [(i, j) for i in range(3) for j in range(3)]


def mark_defs_and_body(h, v, mid, gap=2.5):
    """The woven mark in a 0..100 box (content LO..HI).

    A true weave: at each crossing one thread passes over at full width and the
    thread underneath is cut by a small gap on both sides of it. Crossings with
    (row + column) even have the horizontal thread on top. Gaps are real
    cut-outs (a mask), so the mark works on any background.
    """
    mask = [f'<mask id="{mid}" maskUnits="userSpaceOnUse" x="0" y="0" width="100" height="100">',
            '<rect width="100" height="100" fill="#fff"/>']
    over_h = []
    for i, j in CROSS:
        vx, hy = VX[j], HY[i]
        if (i + j) % 2 == 0:  # horizontal over: cut the vertical above and below
            over_h.append((vx, hy))
            mask.append(f'<rect x="{vx}" y="{hy - gap:g}" width="12" height="{gap:g}" fill="#000"/>')
            mask.append(f'<rect x="{vx}" y="{hy + 12}" width="12" height="{gap:g}" fill="#000"/>')
        else:  # vertical over: cut the horizontal left and right
            mask.append(f'<rect x="{vx - gap:g}" y="{hy}" width="{gap:g}" height="12" fill="#000"/>')
            mask.append(f'<rect x="{vx + 12}" y="{hy}" width="{gap:g}" height="12" fill="#000"/>')
    mask.append("</mask>")
    body = [f'<g mask="url(#{mid})">']
    for hy in HY:
        body.append(f'<rect x="{LO}" y="{hy}" width="{SPAN}" height="12" rx="2" fill="{h}"/>')
    for vx in VX:
        body.append(f'<rect x="{vx}" y="{LO}" width="12" height="{SPAN}" rx="2" fill="{v}"/>')
    for vx, hy in over_h:
        body.append(f'<rect x="{vx - 0.5:g}" y="{hy}" width="13" height="12" fill="{h}"/>')
    body.append("</g>")
    return "\n".join(mask), "\n".join(body)


# ---- wordmark outlines ---------------------------------------------------
font = TTFont(FONT)
gs = font.getGlyphSet()
cmap = font.getBestCmap()
UPM = font["head"].unitsPerEm
CAP = font["OS/2"].sCapHeight or 730
TRACK = 0.08 * UPM  # letter-spacing 0.08em, as in the boards


def wordmark_paths(text, cap_px, x0, baseline):
    """Return (svg path d, xmin, xmax) for text set so its cap height is cap_px."""
    s = cap_px / CAP
    x = 0.0
    parts = []
    xmin, xmax = None, None
    for ch in text:
        g = cmap[ord(ch)]
        pen = SVGPathPen(gs)
        tpen = TransformPen(pen, (s, 0, 0, -s, x0 + x * s, baseline))
        gs[g].draw(tpen)
        parts.append(pen.getCommands())
        bp = BoundsPen(gs)
        gs[g].draw(bp)
        if bp.bounds:
            bx0, _, bx1, _ = bp.bounds
            gx0, gx1 = x0 + (x + bx0) * s, x0 + (x + bx1) * s
            xmin = gx0 if xmin is None else min(xmin, gx0)
            xmax = gx1 if xmax is None else max(xmax, gx1)
        x += font["hmtx"][g][0] + TRACK
    return " ".join(parts), xmin, xmax


def write(path, text):
    full = os.path.join(OUT, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w", encoding="utf-8") as f:
        f.write(text.strip() + "\n")


def svg(view_box, inner, title, w=None, h=None):
    size = f' width="{w}" height="{h}"' if w else ""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{view_box}"{size} role="img" aria-label="{title}">\n'
            f"<title>{title}</title>\n{inner}\n</svg>")


# ---- marks ---------------------------------------------------------------
for name, pal, label in [("on-dark", DARK, "for dark backgrounds"), ("on-light", LIGHT, "for light backgrounds")]:
    defs, body = mark_defs_and_body(pal["emerald"], pal["violet"], "k")
    write(f"logo/kasuri-mark-{name}.svg",
          svg(f"{LO} {LO} {SPAN} {SPAN}", f"<defs>{defs}</defs>\n{body}", f"Kasuri mark, {label}", 256, 256))

for name, color in [("white", "#FFFFFF"), ("black", "#160F2B")]:
    defs, body = mark_defs_and_body(color, color, "k")
    write(f"logo/kasuri-mark-mono-{name}.svg",
          svg(f"{LO} {LO} {SPAN} {SPAN}", f"<defs>{defs}</defs>\n{body}", f"Kasuri mark, one color ({name})", 256, 256))

# ---- lockups and wordmarks ----------------------------------------------
CAP_PX = 33          # wordmark cap height next to the 72-unit mark (about 0.46x)
GAP = 18             # space between mark and wordmark
BASE = 50 + CAP_PX / 2
for name, pal, label in [("on-dark", DARK, "for dark backgrounds"), ("on-light", LIGHT, "for light backgrounds")]:
    defs, body = mark_defs_and_body(pal["emerald"], pal["violet"], "k")
    d, xmin, xmax = wordmark_paths("KASURI", CAP_PX, HI + GAP, BASE)
    shift = (HI + GAP) - xmin  # align the K's ink to the gap
    d, xmin, xmax = wordmark_paths("KASURI", CAP_PX, HI + GAP + shift, BASE)
    width = xmax - LO
    write(f"logo/kasuri-lockup-{name}.svg",
          svg(f"{LO} {LO} {width:.2f} {SPAN}",
              f'<defs>{defs}</defs>\n{body}\n<path d="{d}" fill="{pal["text"]}"/>',
              f"Kasuri logo, {label}", round(width * 4), 256))
    # wordmark alone, tight box
    d2, x0, x1 = wordmark_paths("KASURI", 100, 0, 100)
    write(f"logo/kasuri-wordmark-{name}.svg",
          svg(f"{x0:.2f} 0 {x1 - x0:.2f} 100", f'<path d="{d2}" fill="{pal["text"]}"/>',
              f"Kasuri wordmark, {label}", round((x1 - x0) * 2), 200))

# ---- app icon & favicon ---------------------------------------------------
def tile_icon(gap, mark_px, tile_color, border=True):
    k = mark_px / SPAN
    t = 512 - 50 * k
    defs, body = mark_defs_and_body(DARK["emerald"], DARK["violet"], "k", gap)
    border_el = ('<rect x="100.5" y="100.5" width="823" height="823" rx="185.5" fill="none" '
                 'stroke="#FFFFFF" stroke-opacity="0.08" stroke-width="3"/>') if border else ""
    return (f'<defs>{defs}</defs>\n'
            f'<rect x="100" y="100" width="824" height="824" rx="186" fill="{tile_color}"/>\n{border_el}\n'
            f'<g transform="translate({t:g} {t:g}) scale({k:g})">\n{body}\n</g>')

write("icon/kasuri-app-icon.svg", svg("0 0 1024 1024", tile_icon(2.5, 520, DARK["tile"]), "Kasuri app icon", 1024, 1024))
# small sizes: thicker gaps so the weave still reads
write("icon/kasuri-app-icon-small.svg", svg("0 0 1024 1024", tile_icon(4, 580, DARK["tile"]), "Kasuri app icon (small sizes)", 1024, 1024))

fk = 26 / SPAN
defs, body = mark_defs_and_body(DARK["emerald"], DARK["violet"], "k", 4)
write("icon/favicon.svg",
      svg("0 0 32 32",
          f'<defs>{defs}</defs>\n<rect width="32" height="32" rx="7" fill="{DARK["tile"]}"/>\n'
          f'<g transform="translate({16 - 50 * fk:g} {16 - 50 * fk:g}) scale({fk:g})">\n{body}\n</g>',
          "Kasuri", 32, 32))

# ---- tokens ----------------------------------------------------------------
tokens = {
    "brand": {
        "name": "Kasuri", "kanji": "絣",
        "font": {"wordmark": "JetBrains Mono Bold, uppercase, letter-spacing 0.08em", "text": "Inter"},
    },
    "dark": {
        "background": "#160F2B", "chrome": "#120C24", "sunken": "#130D26",
        "surface": "#1B1433", "surface2": "#221A3D", "surface3": "#2A2148",
        "line": "#2F2650", "text": "#EEEAF8", "text2": "#C9C2DD", "muted": "#A79FC2", "faint": "#6E6491",
        "emerald": "#34D399", "emeraldStrong": "#22C08A", "emeraldText": "#7FE5BE", "onEmerald": "#160F2B",
        "violet": "#9B7BFF", "violetStrong": "#8A66FA", "violetText": "#C4B2FF", "onViolet": "#160F2B",
    },
    "light": {
        "background": "#FFFFFF", "chrome": "#EFECF6", "sunken": "#F7F5FC",
        "surface": "#FFFFFF", "surface2": "#F7F5FC", "surface3": "#EFEBF7",
        "line": "#E4DFF0", "text": "#160F2B", "text2": "#3B3355", "muted": "#625A78", "faint": "#9A93AE",
        "emerald": "#0F7A4B", "emeraldStrong": "#0B6A40", "emeraldThread": "#14955C", "onEmerald": "#FFFFFF",
        "violet": "#4B2FB8", "violetStrong": "#3E259F", "violetText": "#4B2FB8", "onViolet": "#FFFFFF",
    },
    "roles": {
        "emerald": "actions: primary buttons, 'needs you', success",
        "violet": "navigation: active page, selection, focus ring, links",
    },
}
write("tokens/kasuri.json", json.dumps(tokens, indent=2, ensure_ascii=False))

D, L = tokens["dark"], tokens["light"]
css = f"""
/* Kasuri brand tokens — generated with the brand kit (brand/README.md).
   1) Brand primitives (--kasuri-*), usable anywhere.
   2) A drop-in palette for web/src/styles/tokens.css, using the Web UI's own
      token names: dark palette "kasuri" plus a matching light theme. */

:root {{
  --kasuri-midnight: {D['background']};
  --kasuri-emerald: {D['emerald']};
  --kasuri-violet: {D['violet']};
  --kasuri-emerald-deep: {L['emeraldThread']};
  --kasuri-violet-deep: {L['violet']};
  --kasuri-action-light: {L['emerald']};
  --kasuri-paper: {L['sunken']};
}}

/* Dark: :root[data-theme='dark'][data-palette='kasuri'] */
:root[data-theme='dark'][data-palette='kasuri'] {{
  --chrome: {D['chrome']};
  --bg: {D['background']};
  --bg-sunken: {D['sunken']};
  --surface: {D['surface']};
  --surface-2: {D['surface2']};
  --surface-3: {D['surface3']};
  --text: {D['text']};
  --text-2: {D['text2']};
  --muted: {D['muted']};
  --faint: {D['faint']};
  /* emerald = actions */
  --accent: {D['emerald']};
  --accent-strong: {D['emeraldStrong']};
  --accent-soft: rgba(52, 211, 153, 0.14);
  --accent-text: {D['emeraldText']};
  --on-accent: {D['onEmerald']};
  --success: {D['emerald']};
  --success-soft: rgba(52, 211, 153, 0.13);
  /* violet = navigation and focus (new tokens; see the rename plan) */
  --nav-accent: {D['violet']};
  --nav-accent-soft: rgba(155, 123, 255, 0.16);
  --nav-accent-text: {D['violetText']};
  --focus: 0 0 0 2px var(--bg), 0 0 0 4px rgba(155, 123, 255, 0.6);
}}

/* Light: :root[data-theme='light'] with the Kasuri brand */
:root[data-theme='light'][data-brand='kasuri'] {{
  --chrome: {L['chrome']};
  --bg: {L['background']};
  --bg-sunken: {L['sunken']};
  --surface: {L['surface']};
  --surface-2: {L['surface2']};
  --surface-3: {L['surface3']};
  --text: {L['text']};
  --text-2: {L['text2']};
  --muted: {L['muted']};
  --faint: {L['faint']};
  --accent: {L['emerald']};
  --accent-strong: {L['emeraldStrong']};
  --accent-soft: rgba(15, 122, 75, 0.1);
  --accent-text: {L['emerald']};
  --on-accent: {L['onEmerald']};
  --success: {L['emerald']};
  --success-soft: rgba(15, 122, 75, 0.1);
  --nav-accent: {L['violet']};
  --nav-accent-soft: rgba(75, 47, 184, 0.1);
  --nav-accent-text: {L['violetText']};
  --focus: 0 0 0 2px var(--bg), 0 0 0 4px rgba(75, 47, 184, 0.45);
}}
"""
write("tokens/kasuri.css", css)
print("ok")
