"""Drives the admin UI through its main flows against scripts/devrig.sh and
asserts the outcome through the API. Exits non-zero on the first failure."""
import json
import sys
import urllib.request

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:19180/"
H = {"X-Dyrokol-Api": "uitoken"}


def api(path):
    req = urllib.request.Request(BASE + path, headers=H)
    return json.load(urllib.request.urlopen(req))


def put(path, body):
    req = urllib.request.Request(BASE + path, method="PUT", data=json.dumps(body).encode(), headers=H)
    urllib.request.urlopen(req).read()


def ports():
    return {x["port"]: x for x in api("api/status")["inports"]}


def feeders(endpoint_id):
    return {p["port"] for p in api("api/status")["inports"] if p.get("endpoint_id") == endpoint_id}


def check(cond, what):
    print(("ok   " if cond else "FAIL ") + what)
    if not cond:
        sys.exit(1)


with sync_playwright() as p:
    br = p.chromium.launch(executable_path="/usr/bin/chromium", args=["--no-sandbox"])
    pg = br.new_context(viewport={"width": 1440, "height": 1000}).new_page()
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.goto(BASE)

    # wrong token is rejected, right token unlocks
    pg.fill("input[type=password]", "nope")
    pg.click("button[type=submit]")
    pg.wait_for_selector(".form-error")
    check("not valid" in pg.inner_text(".form-error"), "login rejects a wrong token")
    pg.fill("input[type=password]", "uitoken")
    pg.click("button[type=submit]")
    pg.wait_for_selector(".stats-grid")
    check(True, "login accepts the right token")

    # the overview shows the route map
    check(pg.locator(".routemap .rm-node").count() >= 4, "overview shows the route map")

    # a new ingress port is created without a route
    pg.goto(BASE + "#/ingress")
    pg.click("text=Add ingress port >> nth=0")
    pg.fill(".dialog input[type=number]", "18010")
    check(pg.locator(".dialog >> text=Route to").count() == 0, "ingress dialog has no route field")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".toast-success")
    check(18010 in ports() and not ports()[18010].get("route_id"), "ingress created, not routed")

    # duplicate port shows the server error inline and keeps the dialog open
    pg.click("text=Add ingress port >> nth=0")
    pg.fill(".dialog input[type=number]", "18010")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".dialog .form-error")
    check("already used" in pg.inner_text(".dialog .form-error"), "duplicate port error is shown inline")
    pg.keyboard.press("Escape")

    # menu order: ingress, egress, routes
    names = pg.locator(".nav-item").all_inner_texts()
    order = [n.split("\n")[0] for n in names]
    check(order.index("Ingress ports") < order.index("Egress ports") < order.index("Routes"),
          "menu order is Ingress, Egress, Routes")

    # add a route in the new-route row of the Routes table
    pg.goto(BASE + "#/routes")
    pg.wait_for_selector(".routes-table")
    first_endpoint = api("api/status")["endpoints"][0]["id"]
    second_endpoint = api("api/status")["endpoints"][1]["id"]
    row = pg.locator("tr.new-route")
    row.locator("select").nth(0).select_option(label="relay:18010")
    check(not row.locator("button", has_text="Add route").is_enabled(), "add-route needs an egress port too")
    row.locator("select").nth(1).select_option(str(first_endpoint))
    row.locator("button", has_text="Add route").click()
    pg.wait_for_selector(".toast-success")
    pg.wait_for_timeout(500)
    check(ports()[18010].get("endpoint_id") == first_endpoint, "a row adds the route")

    # fan-in is visible as several rows with the same egress port
    check(len(feeders(first_endpoint)) >= 4, f"several ingress ports feed one egress port ({len(feeders(first_endpoint))})")

    # edit the row in place: point it at another egress port
    pg.locator(".routes-table tbody tr").filter(has_text="relay:18010").first.locator("select").nth(1).select_option(str(second_endpoint))
    pg.wait_for_selector(".toast-success")
    pg.wait_for_timeout(500)
    check(ports()[18010].get("endpoint_id") == second_endpoint, "changing the egress select re-points the route")

    # delete the route with the trash button
    pg.locator(".routes-table tbody tr").filter(has_text="relay:18010").first.locator('[aria-label^="Delete route"]').click()
    pg.wait_for_selector(".toast-success")
    pg.wait_for_timeout(500)
    check(not ports()[18010].get("route_id"), "the trash button removes the route")

    # the map layout is saved on the server: drag a port, add a frame and a text
    pg.wait_for_selector(".rm-node")
    node = pg.locator(".rm-node").first
    box = node.bounding_box()
    pg.mouse.move(box["x"] + 30, box["y"] + 20)
    pg.mouse.down()
    pg.mouse.move(box["x"] + 130, box["y"] + 90, steps=6)
    pg.mouse.up()
    pg.locator(".rm-toolbar button", has_text="Frame").click()
    pg.fill(".rm-inspector input", "Test frame")
    pg.locator(".rm-inspector button", has_text="Done").click()
    pg.locator(".rm-toolbar button", has_text="Text").click()
    pg.fill(".rm-inspector input", "Test text")
    pg.wait_for_timeout(1200)
    saved = api("api/map")
    check(len(saved["nodes"]) >= 4, "dragging a port saves the positions of all ports")
    check({i["kind"] for i in saved["items"]} == {"frame", "text"}, "frame and text are saved")
    pg.reload()
    pg.wait_for_selector(".rm-frame")
    check(pg.locator(".rm-frame-label", has_text="Test frame").count() == 1, "the layout survives a reload")
    # the annotations lie under the nodes and the links
    z = pg.evaluate("""() => {
      const order = [...document.querySelector('.rm-world').children].map(e => e.className.baseVal ?? e.className);
      const idx = (re) => order.findIndex((c) => re.test(c));
      return [idx(/rm-frame/), idx(/rm-text/), idx(/rm-lines/), idx(/rm-node/)];
    }""")
    check(z[0] < z[1] < z[2] < z[3], f"layers: frames < texts < links < nodes {z}")
    # zoom, pan and full screen
    def zoom_percent():
        return int(pg.inner_text(".rm-zoom-level").rstrip("%"))

    def world_matrix():
        return pg.evaluate("""() => { const m = new DOMMatrix(getComputedStyle(document.querySelector('.rm-world')).transform);
                                      return [m.a, m.e, m.f]; }""")

    z0 = zoom_percent()
    pg.click('[aria-label="Zoom in"]')
    pg.wait_for_timeout(150)
    check(zoom_percent() > z0, f"zoom in raises the zoom level ({z0}% -> {zoom_percent()}%)")
    pg.click('[aria-label="Zoom out"]')
    pg.click(".rm-zoom-level")
    pg.wait_for_timeout(150)
    check(zoom_percent() == 100, "clicking the level resets the zoom to 100%")
    pg.click('[aria-label="Fit to view"]')
    pg.wait_for_timeout(150)

    vpbox = pg.locator(".rm-viewport").bounding_box()
    scale, ex0, ey0 = world_matrix()
    pg.mouse.move(vpbox["x"] + 4, vpbox["y"] + 4)
    pg.mouse.down()
    pg.mouse.move(vpbox["x"] + 84, vpbox["y"] + 44, steps=5)
    pg.mouse.up()
    scale2, ex1, ey1 = world_matrix()
    check(abs((ex1 - ex0) - 80) < 2 and abs((ey1 - ey0) - 40) < 2 and scale2 == scale,
          "dragging the background pans the map")

    # Ctrl + wheel zooms around the cursor; dragging a port still moves it by the right distance
    pg.click('[aria-label="Fit to view"]')
    pg.wait_for_timeout(150)
    nb = pg.locator(".rm-node").first.bounding_box()
    pg.mouse.move(nb["x"] + nb["width"] / 2, nb["y"] + nb["height"] / 2)
    pg.keyboard.down("Control")
    pg.mouse.wheel(0, -400)
    pg.keyboard.up("Control")
    pg.wait_for_timeout(200)
    zoom = zoom_percent() / 100
    check(zoom > 1.2, f"Ctrl + wheel zooms in ({zoom_percent()}%)")
    before = api("api/map")["nodes"]
    nb = pg.locator(".rm-node").first.bounding_box()
    cx, cy = nb["x"] + nb["width"] / 2, nb["y"] + nb["height"] / 2
    pg.mouse.move(cx, cy)
    pg.mouse.down()
    pg.mouse.move(cx + 100, cy, steps=5)
    pg.mouse.up()
    pg.wait_for_timeout(1200)
    after = api("api/map")["nodes"]
    moved = [after[k]["x"] - before[k]["x"] for k in after if after[k] != before.get(k)]
    check(len(moved) == 1 and abs(moved[0] - 100 / zoom) <= 10,
          f"a port dragged by 100 screen px at {zoom_percent()}% moves by {moved} map px")

    # full screen
    pg.click('[aria-label="Full screen"]')
    pg.wait_for_selector(".routemap.rm-expanded")
    pg.wait_for_timeout(300)
    full = pg.locator(".rm-viewport").bounding_box()
    check(full["width"] > 1200 and full["height"] > 800, f"the map fills the window ({full['width']:.0f}x{full['height']:.0f})")
    pg.locator(".rm-toolbar button", has_text="Auto-arrange").click()
    pg.wait_for_selector(".dialog")
    check(pg.locator(".dialog").is_visible(), "dialogs open above the full-screen map")
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(200)
    check(pg.locator(".routemap.rm-expanded").count() == 1, "Escape closes the dialog first, not the full screen")
    pg.keyboard.press("Escape")
    pg.wait_for_timeout(300)
    check(pg.locator(".routemap.rm-expanded").count() == 0, "Escape leaves the full screen")

    # a new annotation is selected right away, so Delete removes it
    n = len(api("api/map")["items"])
    pg.locator(".rm-toolbar button", has_text="Text").click()
    pg.wait_for_timeout(1200)
    check(len(api("api/map")["items"]) == n + 1, "a new text is saved")
    pg.keyboard.press("Delete")
    pg.wait_for_timeout(1200)
    check(len(api("api/map")["items"]) == n, "Delete removes the selected annotation")
    # remove the test annotations again
    layout = api("api/map")
    layout["items"] = []
    put("api/map", layout)

    # port numbers are editable: ingress
    pg.goto(BASE + "#/ingress")
    pg.locator("tr", has_text=":18010").locator("[aria-label=Actions]").click()
    pg.click("[role=menuitem]:has-text('Edit')")
    pg.fill(".dialog input[type=number]", "18011")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".toast-success")
    pg.wait_for_timeout(500)
    check(18011 in ports() and 18010 not in ports(), "the ingress port number can be edited")
    # ... and a busy number is refused inline
    pg.locator("tr", has_text=":18011").locator("[aria-label=Actions]").click()
    pg.click("[role=menuitem]:has-text('Edit')")
    pg.fill(".dialog input[type=number]", "18000")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".dialog .form-error")
    check("already used" in pg.inner_text(".dialog .form-error"), "a taken port number is refused")
    pg.keyboard.press("Escape")

    # ... and egress: create, edit target host and port, delete
    pg.goto(BASE + "#/egress")
    pg.click("text=Add egress port >> nth=0")
    pg.fill(".dialog input[type=number]", "19999")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".toast-success")
    pg.locator("tr", has_text="127.0.0.1:19999").locator("[aria-label=Actions]").click()
    pg.click("[role=menuitem]:has-text('Edit')")
    pg.fill(".dialog input[type=number]", "19998")
    pg.locator(".dialog input[placeholder='127.0.0.1']").fill("10.9.8.7")
    pg.click(".dialog button[type=submit]")
    pg.wait_for_selector(".toast-success")
    pg.wait_for_timeout(500)
    eps = {(e["host"], e["port"]) for e in api("api/status")["endpoints"]}
    check(("10.9.8.7", 19998) in eps and ("127.0.0.1", 19999) not in eps, "the egress target can be edited")
    pg.locator("tr", has_text="10.9.8.7:19998").locator("[aria-label=Actions]").click()
    pg.click("[role=menuitem]:has-text('Delete')")
    pg.click(".dialog .btn-danger")
    pg.wait_for_timeout(600)

    # delete with confirmation
    pg.goto(BASE + "#/ingress")
    pg.locator("tr", has_text=":18011").locator("[aria-label=Actions]").click()
    pg.click("[role=menuitem]:has-text('Delete')")
    pg.click(".dialog .btn-danger")
    pg.wait_for_timeout(800)
    check(18011 not in ports(), "delete asks for confirmation and deletes")

    # lock returns to the login screen
    pg.click("[aria-label=Lock]")
    pg.wait_for_selector(".login-card")
    check(True, "lock returns to the login screen")
    check(not errors, f"no page errors {errors}")
    br.close()
