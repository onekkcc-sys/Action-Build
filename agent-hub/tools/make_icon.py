"""Generate icon.ico and rsrc_windows_amd64.syso (exe icon resource) for Agent Hub.

Usage: python3 tools/make_icon.py   (run from the agent-hub directory; needs Pillow)
"""
import io
import struct
from PIL import Image, ImageDraw

SIZES = [16, 24, 32, 48, 64, 128, 256]


def draw(size):
    s = 1024
    im = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.rounded_rectangle([0, 0, s - 1, s - 1], radius=224, fill=(196, 83, 47, 255))
    for x, y, a in [(208, 608, 255), (448, 384, 255), (688, 224, 217)]:
        d.rounded_rectangle([x, y, x + 128, 832], radius=48, fill=(255, 255, 255, a))
    return im.resize((size, size), Image.LANCZOS)


def png_bytes(size):
    buf = io.BytesIO()
    draw(size).save(buf, "PNG")
    return buf.getvalue()


images = [(sz, png_bytes(sz)) for sz in SIZES]

# ---- icon.ico ----
ico = struct.pack("<HHH", 0, 1, len(images))
off = 6 + 16 * len(images)
for sz, data in images:
    b = sz if sz < 256 else 0
    ico += struct.pack("<BBBBHHII", b, b, 0, 0, 1, 32, len(data), off)
    off += len(data)
ico += b"".join(d for _, d in images)
open("icon.ico", "wb").write(ico)

# ---- resource section ----
RT_ICON, RT_GROUP_ICON, LANG = 3, 14, 0x0409
grp = struct.pack("<HHH", 0, 1, len(images))
for i, (sz, data) in enumerate(images, 1):
    b = sz if sz < 256 else 0
    grp += struct.pack("<BBBBHHIH", b, b, 0, 0, 1, 32, len(data), i)

# leaves: (type, id, bytes)
leaves = [(RT_ICON, i, d) for i, (_, d) in enumerate(images, 1)] + [(RT_GROUP_ICON, 1, grp)]
types = sorted({t for t, _, _ in leaves})


def dir_hdr(n):
    return struct.pack("<IIHHHH", 0, 0, 0, 0, 0, n)


# layout: root dir, type dirs, name dirs (one per leaf), lang dirs (one per leaf), data entries, data
root_size = 16 + 8 * len(types)
type_sizes = {t: 16 + 8 * sum(1 for x in leaves if x[0] == t) for t in types}
pos = root_size
type_off = {}
for t in types:
    type_off[t] = pos
    pos += type_sizes[t]
lang_off = []
for _ in leaves:
    lang_off.append(pos)
    pos += 16 + 8
entry_off = []
for _ in leaves:
    entry_off.append(pos)
    pos += 16
data_off = []
for _, _, d in leaves:
    pos = (pos + 7) & ~7
    data_off.append(pos)
    pos += len(d)
total = (pos + 7) & ~7

sec = bytearray(total)
sec[0:root_size] = dir_hdr(len(types)) + b"".join(
    struct.pack("<II", t, 0x80000000 | type_off[t]) for t in types)
for t in types:
    idx = [i for i, x in enumerate(leaves) if x[0] == t]
    blob = dir_hdr(len(idx)) + b"".join(
        struct.pack("<II", leaves[i][1], 0x80000000 | lang_off[i]) for i in idx)
    sec[type_off[t]:type_off[t] + len(blob)] = blob
relocs = []
for i, (_, _, d) in enumerate(leaves):
    sec[lang_off[i]:lang_off[i] + 24] = dir_hdr(1) + struct.pack("<II", LANG, entry_off[i])
    sec[entry_off[i]:entry_off[i] + 16] = struct.pack("<IIII", data_off[i], len(d), 0, 0)
    relocs.append(entry_off[i])  # OffsetToData needs the section RVA added
    sec[data_off[i]:data_off[i] + len(d)] = d

# ---- COFF object ----
IMAGE_REL_AMD64_ADDR32NB = 3
hdr_size, sect_hdr = 20, 40
raw_ptr = hdr_size + sect_hdr
reloc_ptr = raw_ptr + len(sec)
sym_ptr = reloc_ptr + 10 * len(relocs)
out = struct.pack("<HHIIIHH", 0x8664, 1, 0, sym_ptr, 1, 0, 0)
out += struct.pack("<8sIIIIIIHHI", b".rsrc", 0, 0, len(sec), raw_ptr, reloc_ptr, 0,
                   len(relocs), 0, 0x40000040)
out += bytes(sec)
out += b"".join(struct.pack("<IIH", r, 0, IMAGE_REL_AMD64_ADDR32NB) for r in relocs)
out += struct.pack("<8sIhHBB", b".rsrc", 0, 1, 0, 3, 0)
out += struct.pack("<I", 4)  # empty string table
open("rsrc_windows_amd64.syso", "wb").write(out)
print("wrote icon.ico and rsrc_windows_amd64.syso", len(out), "bytes")
