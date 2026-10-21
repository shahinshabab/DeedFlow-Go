import struct, random

END, FREE, FATSEC = 0xFFFFFFFE, 0xFFFFFFFF, 0xFFFFFFFD

def build_cfb2(rng, streams, subdirs=None):
    """streams: list of (name, data) at root. subdirs: list of (dirname, [(name,data)])."""
    SS, MS = 512, 64
    secs = []   # sector payloads
    fat = []
    def alloc(data):
        n = max(1, -(-len(data) // SS)) if data else 0
        if n == 0: return END
        start = len(secs)
        for i in range(n):
            chunk = data[i*SS:(i+1)*SS].ljust(SS, b'\0')
            secs.append(chunk)
            fat.append(start + i + 1 if i < n - 1 else END)
        return start
    ents = []  # dict(name,type,start,size,left,right,child)
    mini = bytearray(); minifat = []
    def mini_alloc(data):
        n = -(-len(data) // MS)
        start = len(mini) // MS
        for i in range(n):
            mini.extend(data[i*MS:(i+1)*MS].ljust(MS, b'\0'))
            minifat.append(start + i + 1 if i < n - 1 else END)
        return start
    def add_stream(name, data):
        if len(data) == 0:
            return dict(name=name, type=2, start=END, size=0, left=FREE, right=FREE, child=FREE)
        if len(data) < 4096:
            st = mini_alloc(data)
        else:
            st = alloc(data)
        return dict(name=name, type=2, start=st, size=len(data), left=FREE, right=FREE, child=FREE)
    root = dict(name='Root Entry', type=5, start=END, size=0, left=FREE, right=FREE, child=FREE)
    ents.append(root)
    def link(children_idx):
        # balanced tree over indices
        if not children_idx: return FREE
        mid = len(children_idx) // 2
        i = children_idx[mid]
        ents[i]['left'] = link(children_idx[:mid])
        ents[i]['right'] = link(children_idx[mid+1:])
        return i
    top = []
    for name, data in streams:
        ents.append(add_stream(name, data)); top.append(len(ents) - 1)
    for dname, items in (subdirs or []):
        d = dict(name=dname, type=1, start=END, size=0, left=FREE, right=FREE, child=FREE)
        ents.append(d); di = len(ents) - 1; top.append(di)
        sub = []
        for name, data in items:
            ents.append(add_stream(name, data)); sub.append(len(ents) - 1)
        d['child'] = link(sub)
    rng.shuffle(top)
    root['child'] = link(top)
    # ministream container
    if mini:
        root['start'] = alloc(bytes(mini)); root['size'] = len(mini)
    # mini fat
    mf = b''.join(struct.pack('<I', v) for v in minifat)
    mf_start = alloc(mf) if mf else END
    n_mf = -(-len(mf) // SS) if mf else 0
    # directory
    def entry(e):
        nm = (e['name'] + '\0').encode('utf-16-le')
        b = nm.ljust(64, b'\0') + struct.pack('<HBB', len(nm), e['type'], 1)
        b += struct.pack('<III', e['left'], e['right'], e['child']) + b'\0' * 36
        b += struct.pack('<III', e['start'], e['size'], 0)
        return b
    dirb = b''.join(entry(e) for e in ents)
    while len(dirb) % SS: dirb += entry(dict(name='', type=0, start=0, size=0, left=FREE, right=FREE, child=FREE)).replace(b'\x01', b'\0', 1) if False else b'\0' * 128
    dir_start = alloc(dirb)
    # FAT sectors
    n = len(secs); f = 1
    while n + f > 128 * f: f += 1
    fat_secs = list(range(n, n + f))
    for i in range(f): fat.append(FATSEC)
    fat += [FREE] * (128 * f - len(fat))
    fatb = struct.pack('<%dI' % len(fat), *fat)
    for i in range(f): secs.append(fatb[i*SS:(i+1)*SS])
    hdr = bytes.fromhex('D0CF11E0A1B11AE1') + b'\0' * 16 + struct.pack('<HHHHH', 0x3E, 3, 0xFFFE, 9, 6) + b'\0' * 6
    hdr += struct.pack('<IIIIIIIII', 0, f, dir_start, 0, 4096, mf_start, n_mf, END, 0)
    difat = fat_secs + [FREE] * (109 - len(fat_secs))
    hdr += struct.pack('<109I', *difat)
    assert len(hdr) == 512
    return hdr + b''.join(secs)
