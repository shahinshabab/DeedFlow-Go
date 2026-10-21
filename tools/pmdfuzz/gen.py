import sys, random, struct, importlib.util, os
spec = importlib.util.spec_from_file_location('samples', ''+os.environ.get('SAMPLES_PY','samples.py')+'')
S = importlib.util.module_from_spec(spec); spec.loader.exec_module(S)

def build(rng, big=False):
    e = '>' if big else '<'
    toc = []  # entries: dict(kind, rt, num, payload, sub)
    def add(rt, num, payload):
        toc.append(dict(kind='simple', rt=rt, num=num, payload=payload))
        return len(toc) - 1
    def s16(v): return struct.pack(e + 'h', v)
    def u16(v): return struct.pack(e + 'H', v & 0xffff)
    def u32(v): return struct.pack(e + 'I', v & 0xffffffff)
    def pt(): return s16(rng.randint(-6000, 6000)) + s16(rng.randint(-8000, 8000))
    W = rng.choice([11906, 11000, 8000, 0, 100]); H = rng.choice([16836, 15000, 9000, 0, 50])
    gi = bytearray(rng.randbytes(2496) if rng.random() < 0.1 else bytes(2496))
    gi[0] = rng.choice([0, 2, 0x40, 0x42])
    gi[0x36:0x3e] = struct.pack(e + 'hhhh', -W // 2, -H // 2, W - W // 2, H - H // 2)
    add(0x18, 1, bytes(gi))
    page_idx = add(0x05, 0, b'')
    nfonts = rng.randint(0, 4)
    names = [rng.choice([b'Times New Roman', b'ML-TTKarthika', b'Arial\xe9', b'A"B\\C', b'']) for _ in range(nfonts)]
    add(0x13, nfonts, b''.join(n.ljust(94, b'\0') for n in names))
    ncol = rng.randint(0, 5)
    cols = b''
    for i in range(ncol):
        c = bytearray(rng.randbytes(210))
        c[0x22] = rng.choice([0x18, 0x08, 0x10, 0x18, 0x00, 0x20])
        cols += bytes(c)
    add(0x15, ncol, cols)
    nx = rng.randint(0, 4)
    xs = b''
    for i in range(nx):
        rot = rng.choice([0, 0, 30000, -45000 & 0xffffffff, 90000, rng.randint(0, 360000)])
        skew = rng.choice([0, 0, 15000, 80000, rng.randint(0, 100000)])
        xs += u32(rot) + u32(skew) + b'\0\0' + pt() + pt() + pt() + u32(rng.choice([0, 1, 2, 3, i + 1]))
    xf_idx = add(0x28, nx, xs)
    tb_idx = add(0x1a, 0, b'')
    npages = rng.randint(0, 4)
    blocks = []
    page_payload = b''
    block_id = rng.choice([100, 0xffffffff - 3, 5])
    for pg in range(npages):
        recs = b''
        nshapes = rng.randint(0, 5)
        for si in range(nshapes):
            r = bytearray(rng.randbytes(258) if rng.random() < 0.3 else bytes(258))
            r[0] = rng.choice([1, 3, 4, 5, 6, 10, 12, 1, 4, 12, 7])
            r[6:10] = pt(); r[10:14] = pt()
            r[28:32] = u32(rng.choice([0, 1, 2, 3, 4, 0xffffffff]))
            if r[0] == 1:
                block_id += 1
                r[32:36] = u32(block_id)
                text = bytes(rng.choice(b'ab c\t\r  Z\x01\xa9\xe9\x80\xff') for _ in range(rng.randint(0, 40)))
                text_seq = add(0x0d, len(text), text)
                nc = rng.randint(0, 4)
                chars = b''
                for _ in range(nc):
                    flags = rng.randint(0, 0x1fff) if rng.random() < .5 else rng.choice([0, 1, 0x200, 0x400, 0x800, 0x1000])
                    chars += (u16(rng.choice([0, 1, 5, 10, 30, 65535, rng.randint(0, 40)])) + u16(rng.randint(0, nfonts + 1))
                              + u16(rng.randint(0, 300)) + u16(rng.choice([0, 0, 120])) + u16(rng.randint(0, ncol + 1))
                              + u16(flags) + b'\0' * 4 + s16(rng.choice([0, 0, 50, -50])) + b'\0\0'
                              + u16(rng.randint(0, 800)) + u16(rng.randint(0, 800)) + u16(rng.randint(0, 800)) + b'\0\0' + u16(rng.choice([100, 100, 50, 0, 300, 500])))
                chars_seq = add(0x1c, nc, chars)
                npara = rng.randint(0, 3)
                paras = b''
                for _ in range(npara):
                    p = bytearray(rng.randbytes(80) if rng.random() < .4 else bytes(80))
                    p[0:2] = u16(rng.choice([0, 1, 4, 10, 65535, rng.randint(0, 50)]))
                    p[3] = rng.randint(0, 5)
                    if rng.random() < .6:
                        p[34 - 0:36] = u16(rng.choice([0, 1, 2]))  # random-ish
                    paras += bytes(p)
                para_seq = add(0x0b, npara, paras + b'\0' * 64)
                tb = bytearray(36)
                tb[0:12] = u16(0) + u16(0) + u16(text_seq) + u16(chars_seq) + u16(para_seq) + u16(0)
                tb[0x20:0x24] = u32(block_id)
                blocks.append(bytes(tb))
            elif r[0] == 12:
                n = rng.randint(0, 6)
                ls = b''.join(pt() for _ in range(n))
                # linesets: 4-byte records
                seq = add(0x11, n, ls)
                r[46:48] = u16(seq)
                r[56] = rng.choice([0, 1, 3, 0, 3])
            elif r[0] in (6, 10):
                tiff = bytes(rng.randbytes(rng.randint(0, 30)))
                s1 = add(0x0e, len(tiff), tiff)
                add(0x0e, 3, b'xyz')
                r[48:50] = u16(rng.choice([s1, s1, s1 + 5]))
            recs += bytes(r)
        seq = add(0x19, nshapes, recs)
        p = bytearray(472)
        p[2:4] = u16(seq if rng.random() < .95 else seq + 7)
        page_payload += bytes(p)
    toc[page_idx]['num'] = npages
    toc[page_idx]['payload'] = page_payload
    toc[tb_idx]['num'] = len(blocks)
    toc[tb_idx]['payload'] = b''.join(blocks)
    out = bytearray(0x100)
    out[6:8] = b'\x99\xff' if big else b'\xff\x99'
    offs = []
    for t in toc:
        off = len(out)
        out += t['payload'] + b'\0' * 256
        offs.append(off)
    def ent16(t, off):
        return struct.pack(e + 'BBHIH', 0, t['rt'], t['num'], off, 0) + struct.pack('BB4x', 0, 0)
    def ent10(rt, num, off):
        return struct.pack(e + 'BBHIH', 0, rt, num, off, 0)
    top = []  # bytes of top-level entries
    k = 0
    while k < len(toc):
        t = toc[k]
        r = rng.random()
        if r < 0.08 and t['rt'] in (0x13, 0x15, 0x28):
            sub = ent10(t['rt'], t['num'], offs[k])
            sub_off = len(out); out += sub + b'\0' * 32
            top.append(struct.pack(e + 'BBHIH', 0, 1, 1, sub_off, 0) + struct.pack('BB4x', 0, t['rt']))
            k += 1
        elif r < 0.16 and k + 2 < len(toc):
            n = rng.randint(1, min(4, len(toc) - k))
            sub = b''.join(ent16(toc[k + m], offs[k + m]) for m in range(n))
            sub_off = len(out); out += sub + b'\0' * 32
            top.append(ent10(0, n, sub_off))
            k += n
        else:
            top.append(ent16(t, offs[k]))
            k += 1
    toc_off = len(out)
    out += b''.join(top) + b'\0' * 64
    out[0x2E:0x30] = struct.pack(e + 'H', len(top))
    out[0x30:0x34] = struct.pack(e + 'I', toc_off)
    return bytes(out)

def mutate(rng, data):
    d = bytearray(data)
    for _ in range(rng.randint(1, 6)):
        k = rng.random()
        if k < .6:
            d[rng.randrange(len(d))] = rng.randrange(256)
        elif k < .8:
            i = rng.randrange(len(d)); d[i:i + 4] = rng.randbytes(4)
        else:
            d = d[:rng.randrange(len(d))]
            if not d: d = bytearray(b'\0')
    return bytes(d)

if __name__ == '__main__':
    out, n, seed = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
    os.makedirs(out, exist_ok=True)
    for i in range(n):
        rng = random.Random(seed * 100003 + i)
        raw = build(rng, big=rng.random() < 0.2)
        if rng.random() < .3:
            raw = mutate(rng, raw)
        open(os.path.join(out, 'f%05d.pmd' % i), 'wb').write(S._cfb('PageMaker', raw))
