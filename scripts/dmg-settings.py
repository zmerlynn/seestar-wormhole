import os

format = "UDZO"
files = [
    os.path.join(defines["stage"], name)
    for name in ("seestar-copy-cli", "LICENSE", "NOTICE")
]
window_rect = ((100, 100), (480, 200))
default_view = "icon-view"
icon_size = 64
text_size = 12
icon_locations = {
    "seestar-copy-cli": (96, 80),
    "LICENSE": (240, 80),
    "NOTICE": (384, 80),
}
