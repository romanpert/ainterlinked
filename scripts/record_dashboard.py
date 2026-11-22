# Records the ainterlinked dashboard (one headless browser page, nothing else
# on screen) to a video, for demos. Used by scripts/cross-live.mjs.
#
#   python scripts/record_dashboard.py <url> <out_dir> <stop_file> [max_seconds]
#
# Records until <stop_file> exists (or max_seconds pass), then saves
# dashboard.webm plus final screenshots in dark and light mode.
import os
import shutil
import sys
import time

from playwright.sync_api import sync_playwright

url, out, stop = sys.argv[1:4]
limit = float(sys.argv[4]) if len(sys.argv) > 4 else 600.0
size = {"width": 1440, "height": 900}
os.makedirs(out, exist_ok=True)

with sync_playwright() as p:
    browser = p.chromium.launch()
    ctx = browser.new_context(viewport=size, record_video_dir=out, record_video_size=size, color_scheme="dark")
    page = ctx.new_page()
    page.goto(url)
    print("recording", flush=True)
    t0 = time.time()
    while not os.path.exists(stop) and time.time() - t0 < limit:
        page.wait_for_timeout(500)
    page.wait_for_timeout(4000)  # let the last refresh land on screen
    page.screenshot(path=os.path.join(out, "dashboard-final-dark.png"), full_page=True)
    page.emulate_media(color_scheme="light")
    page.wait_for_timeout(500)
    page.screenshot(path=os.path.join(out, "dashboard-final-light.png"), full_page=True)
    video = page.video.path()
    ctx.close()
    browser.close()
    shutil.move(video, os.path.join(out, "dashboard.webm"))
    print("saved", os.path.join(out, "dashboard.webm"), flush=True)
