import os

format = "UDZO"
files = [
    os.path.join(defines["stage"], name)
    for name in ("seestar-copy.app", "seestar-copy-cli", "LICENSE", "NOTICE")
]
symlinks = {"Applications": "/Applications"}
background = "builtin-arrow"
window_rect = ((100, 100), (640, 360))
default_view = "icon-view"
icon_size = 64
text_size = 12
# The builtin arrow occupies (266, 81) through (391, 151) on a 640 x 240 canvas.
icon_locations = {
    "seestar-copy.app": (144, 116),
    "Applications": (496, 116),
    "seestar-copy-cli": (144, 264),
    "LICENSE": (320, 264),
    "NOTICE": (496, 264),
}
