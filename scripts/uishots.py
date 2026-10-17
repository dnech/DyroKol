"""Screenshots every admin UI page (dark, light, mobile) against scripts/devrig.sh.

Usage: python3 scripts/uishots.py [outdir]   (default /tmp/uishots)
Prints console errors and page exceptions, if any.
"""
import os
import sys

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:19180/"
OUT = (sys.argv[1] if len(sys.argv) > 1 else "/tmp/uishots") + "/"
os.makedirs(OUT, exist_ok=True)
errors = []

with sync_playwright() as p:
    br = p.chromium.launch(executable_path="/usr/bin/chromium", args=["--no-sandbox"])
    for theme, width, tag in [("dark", 1440, "d"), ("light", 1440, "l"), ("dark", 390, "m")]:
        ctx = br.new_context(viewport={"width": width, "height": 900})
        ctx.add_init_script(f"localStorage.setItem('dyrokol_theme','{theme}')")
        pg = ctx.new_page()
        pg.on("console", lambda m, t=tag: errors.append(f"[{t}] {m.type}: {m.text}") if m.type in ("error", "warning") else None)
        pg.on("pageerror", lambda e, t=tag: errors.append(f"[{t}] pageerror: {e}"))
        pg.goto(BASE)
        pg.wait_for_selector(".login-card")
        if tag == "d":
            pg.screenshot(path=OUT + "login.png")
        pg.fill("input[type=password]", "uitoken")
        pg.click("button[type=submit]")
        pg.wait_for_selector(".stats-grid")
        pg.wait_for_timeout(1500)
        names = ["overview", "machines", "ingress", "egress", "activity", "logs", "settings"]
        for name in names if tag != "m" else ["overview", "ingress"]:
            pg.goto(BASE + "#/" + name)
            pg.wait_for_timeout(900)
            pg.screenshot(path=f"{OUT}{tag}-{name}.png", full_page=True)
        if tag == "d":
            pg.goto(BASE + "#/overview")
            pg.wait_for_timeout(800)
            box = pg.query_selector(".chart-plot svg").bounding_box()
            pg.mouse.move(box["x"] + box["width"] - 120, box["y"] + 100)
            pg.wait_for_timeout(300)
            pg.screenshot(path=OUT + "d-hover.png", clip={
                "x": box["x"] - 20, "y": box["y"] - 60, "width": box["width"] + 40, "height": box["height"] + 80})
            pg.goto(BASE + "#/ingress")
            pg.wait_for_timeout(500)
            pg.click("text=Add ingress port >> nth=0")
            pg.wait_for_timeout(400)
            pg.screenshot(path=OUT + "d-dialog-ingress.png")
            pg.keyboard.press("Escape")
            pg.goto(BASE + "#/egress")
            pg.wait_for_timeout(500)
            pg.click("text=Add egress port >> nth=0")
            pg.wait_for_timeout(300)
            pg.click("text=SOCKS5 exit")
            pg.wait_for_timeout(200)
            pg.screenshot(path=OUT + "d-dialog-egress.png")
            pg.keyboard.press("Escape")
            pg.goto(BASE + "#/machines")
            pg.wait_for_timeout(500)
            pg.click("[aria-label=Actions] >> nth=1")
            pg.wait_for_timeout(200)
            pg.screenshot(path=OUT + "d-menu.png")
            pg.keyboard.press("Escape")
            pg.click("text=Add machine")
            pg.fill(".dialog input", "test-box")
            pg.click(".dialog button[type=submit]")
            pg.wait_for_timeout(700)
            pg.screenshot(path=OUT + "d-token.png")
        if tag == "m":
            pg.click("[aria-label=Menu]")
            pg.wait_for_timeout(300)
            pg.screenshot(path=OUT + "m-nav.png")
        ctx.close()
    br.close()

print("\n".join(errors) or "no console errors")
