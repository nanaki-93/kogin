"""Render PNG / ICO / ICNS files and a preview sheet from the brand SVGs."""
import base64
import io
import os
import sys

from PIL import Image
from playwright.sync_api import sync_playwright

OUT = sys.argv[1]
PREVIEW = sys.argv[2]


def data_uri(path):
    with open(path, "rb") as f:
        return "data:image/svg+xml;base64," + base64.b64encode(f.read()).decode()


def render(page, svg_path, size, out_png):
    page.set_viewport_size({"width": size, "height": size})
    page.set_content(
        f'<html><body style="margin:0;background:transparent">'
        f'<img src="{data_uri(svg_path)}" width="{size}" height="{size}" style="display:block"></body></html>')
    page.wait_for_timeout(50)
    png = page.screenshot(omit_background=True, clip={"x": 0, "y": 0, "width": size, "height": size})
    with open(out_png, "wb") as f:
        f.write(png)
    return Image.open(io.BytesIO(png)).convert("RGBA")


icon_dir = os.path.join(OUT, "icon")
with sync_playwright() as p:
    browser = p.chromium.launch()
    page = browser.new_page(device_scale_factor=1)
    big = os.path.join(icon_dir, "kogin-app-icon.svg")
    small = os.path.join(icon_dir, "kogin-app-icon-small.svg")
    images = {}
    for size in (1024, 512, 256, 128):
        images[size] = render(page, big, size, os.path.join(icon_dir, f"kogin-app-icon-{size}.png"))
    for size in (64, 32, 16):
        images[size] = render(page, small, size, os.path.join(icon_dir, f"kogin-app-icon-{size}.png"))

    fav = os.path.join(icon_dir, "favicon.svg")
    favs = [render(page, fav, s, os.path.join("/tmp", f"fav-{s}.png")) for s in (16, 32, 48)]
    favs[-1].save(os.path.join(icon_dir, "favicon.ico"), sizes=[(16, 16), (32, 32), (48, 48)],
                  append_images=favs[:-1])

    images[1024].save(os.path.join(icon_dir, "kogin.icns"),
                      append_images=[images[s] for s in (512, 256, 128, 64, 32, 16)])

    # preview sheet
    logo = os.path.join(OUT, "logo")
    def img(name, h):
        return f'<img src="{data_uri(os.path.join(logo, name))}" style="height:{h}px">'
    def ico(name, s):
        return f'<img src="{data_uri(os.path.join(icon_dir, name))}" width="{s}" height="{s}">'
    html = f"""
<html><body style="margin:0;font:13px Inter,system-ui,sans-serif">
<div style="display:grid;grid-template-columns:1fr 1fr;width:1400px">
  <div style="background:#160F2B;padding:48px;display:flex;flex-direction:column;gap:36px;color:#A79FC2">
    {img('kogin-lockup-on-dark.svg', 96)}
    <div style="display:flex;gap:36px;align-items:center">{img('kogin-mark-on-dark.svg', 120)}{img('kogin-mark-mono-white.svg', 120)}{img('kogin-wordmark-on-dark.svg', 44)}</div>
    <div style="display:flex;gap:24px;align-items:flex-end">{ico('kogin-app-icon.svg', 160)}{ico('kogin-app-icon-small.svg', 64)}{ico('kogin-app-icon-small.svg', 32)}{ico('favicon.svg', 16)}</div>
  </div>
  <div style="background:#F7F5FC;padding:48px;display:flex;flex-direction:column;gap:36px;color:#625A78">
    {img('kogin-lockup-on-light.svg', 96)}
    <div style="display:flex;gap:36px;align-items:center">{img('kogin-mark-on-light.svg', 120)}{img('kogin-mark-mono-black.svg', 120)}{img('kogin-wordmark-on-light.svg', 44)}</div>
    <div style="display:flex;gap:24px;align-items:flex-end">{ico('kogin-app-icon.svg', 160)}{ico('kogin-app-icon-small.svg', 64)}{ico('kogin-app-icon-small.svg', 32)}{ico('favicon.svg', 16)}</div>
  </div>
</div></body></html>"""
    page.set_viewport_size({"width": 1400, "height": 600})
    page.set_content(html)
    page.wait_for_timeout(200)
    page.screenshot(path=PREVIEW, full_page=True)
    browser.close()
print("rendered")
