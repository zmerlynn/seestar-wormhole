#!/bin/sh
# Builds the browse page's fixture tree under $1, at image build time, for
# the browser tests in test/browser. Its files have real content (a JPEG, a
# FITS header, text), so it is apart from the contract tree of tree.txt.
set -eu
root=$1
m31="$root/MyWorks/M 31"
long=Light_NGC7000_NorthAmericaNebula_mosaic_panel_01_of_12_10.0s_IRCUT_20261029-033119
mkdir -p "$m31/Notes" "$root/Long names"
printf 'Seestar session notes\nM 31, 10 s subs, IR cut filter.\n' > "$root/00-notes.txt"
: > "$root/empty.txt"
printf 'Last session of the month.\n' > "$m31/Notes/log.txt"
printf 'A file whose name has no place to break.\n' > "$root/Long names/${long}_${long}.txt"
# A FITS primary header of 2,880 bytes, 80-byte cards up to END padded with
# blank cards, then one block of data.
{
 for card in \
  'SIMPLE  =                    T / conforms to FITS standard' \
  'BITPIX  =                   16' \
  'NAXIS   =                    2' \
  'NAXIS1  =                    4' \
  'NAXIS2  =                    4' \
  "OBJECT  = 'M 31    '" \
  'EXPTIME =                 10.0' \
  'END'; do
  printf '%-80s' "$card"
 done
 i=8
 while [ "$i" -lt 36 ]; do printf '%80s' ''; i=$((i + 1)); done
 head -c 2880 /dev/zero
} > "$m31/Stacked_M 31.fit"
# Two small JPEGs, 160x120 and its 48x36 preview, made with Go's image/jpeg.
base64 -d > "$m31/Stacked_M 31.jpg" <<'JPEG'
/9j/2wCEABALDA4MChAODQ4SERATGCgaGBYWGDEjJR0oOjM9PDkzODdASFxOQERXRTc4UG1RV19i
Z2hnPk1xeXBkeFxlZ2MBERISGBUYLxoaL2NCOEJjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2Nj
Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY//AABEIAHgAoAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAA
AAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGh
CCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hp
anN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV
1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQAC
AQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXx
FxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqS
k5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T1
9vf4+fr/2gAMAwEAAhEDEQA/AOKooorUQUUUUAFFFFABRRRQAUUUUAFFFFABRRRQAUUUUAFFFFAB
RRRQAUUtGKAEop2KXFADcUYp+2l20gI8UYqTZS7KAI8UYqXZR5dAyLFGKl8ujy6AIcUYqXZRsoER
YoxUm2k20AMxSU/FJimA2ilxRigBwFKFpwWpFWkMjC08JUqx1KsVK4FcR08RVcSH2qZLf2pXHYoC
H2p4gPpWklt7VMtp7UrhYyRbn0pfs59K2RZ+1PFn7Ucw7GH9nPpSG3PpW79j9qabP2o5gsYRgPpT
TD7Vttae1Qva+1FxWMcxUwx1qvb+1QPB7U7hYzilMKVeaKomjp3EVCtIRU7JTCtMCRRUqLTFqZKQ
EiJVmOOo46tRVLGSRxVajg9qZFirsWKlsYR249KtJbD0p0QFW4wKlsohW1HpTxaD0q4gFSACpuOx
QNoPSo2tR6VqECo3AouFjJe2HpVaS3HpWvIBVSUCqTEZEkHtVWSKtWUCqUuKpMRmyR1WdKvy1Vkq
kSU3WoWFWXqBqoQ1TUyNVUNUitQBdR6sxyVmrJUyS0rDNaOWrcUwrFSb3qdLj3qWh3N6OcetWo7g
etc8l171Mt370uUdzoluR61ILoetc6t5708XvvU8o7m+boetRtcj1rE+2+9NN570coXNd7ketVZJ
x61mtd+9Qvde9PlFcvSzVUklqs9x71Xef3qkhXJpJKrO9MeWoWkqrCFdqiY0jPUZamIjBpwao80u
aYiUPTw9V80u6gZaEtPE3vVPdS76LAXhP708XB9az99L5lKwGiLn3p32n3rN8yl8yiwXNH7T70hu
fes/zKTzKLBcvm496YZ/eqfmUm+iwFkze9MMtV99Jup2AmMlML1HupM0APLU0mkzSZoEJRRRTAKX
NJRQAuaM0lFAC5pc02igB2aM02igB2aM02igB2aM02igBc0ZpKKAFzSUUUAFFFFABRRRQAUUUUAF
FFFABRRRQAUUUUAFFFFABRRRQAUUUUAFFFFABRRRQB//2Q==
JPEG
base64 -d > "$m31/Stacked_M 31_thn.jpg" <<'JPEG'
/9j/2wCEABALDA4MChAODQ4SERATGCgaGBYWGDEjJR0oOjM9PDkzODdASFxOQERXRTc4UG1RV19i
Z2hnPk1xeXBkeFxlZ2MBERISGBUYLxoaL2NCOEJjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2Nj
Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY//AABEIACQAMAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAA
AAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGh
CCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hp
anN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV
1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQAC
AQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXx
FxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqS
k5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T1
9vf4+fr/2gAMAwEAAhEDEQA/AOKoxU8cBarsOmvJ0FaXCxmYNGDXQR6FIw+6aV9BkUfdNLmQWOdx
RWtNpjx9RVGS3KdqdwsWbaRQRmt2xuoVxnFcmHIqVbhl6Gk43BM9FttRtgozinT6laleMV56L6Qf
xGg30h/iNR7MrmOmvruFs4xWDdSqScVTa5dupqIyE1ajYlsZRRRVCCiiigAooooA/9k=
JPEG
find "$root" -exec touch -h -d @1761700000 {} +
chmod -R a+rX,go-w "$root"
