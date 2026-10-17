"""Regenerates the screenshots used by docs/ into docs/img/ against scripts/devrig.sh.

Usage: python3 scripts/docshots.py
"""
import json
import os
import urllib.request

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:19180/"
OUT = os.path.join(os.path.dirname(__file__), "..", "docs", "img") + "/"
os.makedirs(OUT, exist_ok=True)


def call(method, path, body=None):
    req = urllib.request.Request(BASE + path, method=method, headers={"X-Dyrokol-Api": "uitoken"},
                                 data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req) as resp:
        return json.load(resp) if resp.status == 200 else None


def arrange_map():
    """Lays the demo map out as it would be drawn by hand: frames and a note under the ports."""
    st = call("GET", "api/status")
    nodes = {}
    relay = [p for p in st["inports"] if p["machine"] == st["relay_label"]]
    for i, p in enumerate(sorted(relay, key=lambda p: p["port"])):
        nodes[f"in:{p['id']}"] = {"x": 40, "y": 70 + i * 70}
    others = [p for p in st["inports"] if p["machine"] != st["relay_label"]]
    for i, p in enumerate(others):
        nodes[f"in:{p['id']}"] = {"x": 40, "y": 400 + i * 70}
    ends = sorted(st["endpoints"], key=lambda e: e["id"])
    ys = {}
    for e in ends:
        ys[e["id"]] = 100 if e["machine"] == st["relay_label"] and e["type"] == "direct" else 200 \
            if e["machine"] == st["relay_label"] else 400
        nodes[f"eg:{e['id']}"] = {"x": 700, "y": ys[e["id"]]}
    call("PUT", "api/map", {"nodes": nodes, "items": [
        {"id": "f-relay", "kind": "frame", "x": 20, "y": 20, "w": 272, "h": 350, "text": "Relay: public ports", "color": "blue"},
        {"id": "f-home", "kind": "frame", "x": 680, "y": 350, "w": 272, "h": 110, "text": "Home network", "color": "green"},
        {"id": "t-1", "kind": "text", "x": 330, "y": 330, "text": "three ports, one service", "color": "neutral", "size": "M"},
    ]})


def login(pg):
    pg.goto(BASE)
    pg.wait_for_selector(".login-card")
    pg.fill("input[type=password]", "uitoken")
    pg.click("button[type=submit]")
    pg.wait_for_selector(".stats-grid")
    pg.wait_for_timeout(1500)


def page(pg, name):
    pg.goto(BASE + "#/" + name)
    pg.wait_for_timeout(900)


with sync_playwright() as p:
    br = p.chromium.launch(executable_path="/usr/bin/chromium", args=["--no-sandbox"])
    for theme, suffix in [("dark", ""), ("light", "-light")]:
        ctx = br.new_context(viewport={"width": 1440, "height": 1100})
        ctx.add_init_script(f"localStorage.setItem('dyrokol_theme','{theme}')")
        pg = ctx.new_page()
        login(pg)
        if suffix:
            page(pg, "overview")
            pg.screenshot(path=OUT + "overview-light.png")
            ctx.close()
            continue

        arrange_map()
        for name in ("overview", "machines", "ingress", "routes", "settings"):
            page(pg, name)
            pg.screenshot(path=OUT + name + ".png")

        page(pg, "ingress")
        pg.click("text=Add ingress port >> nth=0")
        pg.wait_for_timeout(400)
        pg.fill(".dialog input[type=number]", "8080")
        pg.screenshot(path=OUT + "ingress-dialog.png")
        pg.keyboard.press("Escape")

        page(pg, "egress")
        pg.click("text=Add egress port >> nth=0")
        pg.wait_for_timeout(300)
        pg.screenshot(path=OUT + "egress-dialog.png")
        pg.keyboard.press("Escape")

        page(pg, "machines")
        pg.click("text=Add machine")
        pg.fill(".dialog input", "home-nas-2")
        pg.click(".dialog button[type=submit]")
        pg.wait_for_selector(".copy-field")
        pg.wait_for_timeout(300)
        pg.screenshot(path=OUT + "token-dialog.png")
        ctx.close()
    br.close()
print("screenshots written to", os.path.normpath(OUT))
