// A small PNG codec, Node only, for the screenshot gate (item 2ga) and the
// etching pipeline (item 2cz). It reads the PNGs browsers and image editors
// write — 8-bit, non-interlaced, grey, grey+alpha, RGB, RGBA or palette — and
// writes RGBA or grey+alpha with fixed settings, so the same pixels always
// encode to the same bytes.
import { deflateSync, inflateSync } from "node:zlib";

const SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
const CHANNELS = { 0: 1, 2: 3, 3: 1, 4: 2, 6: 4 };

// decodePNG returns { width, height, data } with data as RGBA bytes.
export function decodePNG(buffer) {
  if (!buffer.subarray(0, 8).equals(SIGNATURE)) throw new Error("not a PNG");
  let offset = 8, width = 0, height = 0, color = 0, palette = null, alpha = null;
  const data = [];
  while (offset < buffer.length) {
    const length = buffer.readUInt32BE(offset);
    const type = buffer.toString("latin1", offset + 4, offset + 8);
    const chunk = buffer.subarray(offset + 8, offset + 8 + length);
    if (type === "IHDR") {
      width = chunk.readUInt32BE(0); height = chunk.readUInt32BE(4);
      const depth = chunk[8], interlace = chunk[12];
      color = chunk[9];
      if (depth !== 8 || interlace !== 0 || !(color in CHANNELS)) throw new Error(`unsupported PNG: depth ${depth}, colour type ${color}, interlace ${interlace}`);
    } else if (type === "PLTE") palette = chunk;
    else if (type === "tRNS") alpha = chunk;
    else if (type === "IDAT") data.push(chunk);
    else if (type === "IEND") break;
    offset += 12 + length;
  }
  const channels = CHANNELS[color];
  const raw = inflateSync(Buffer.concat(data));
  const stride = width * channels, out = new Uint8Array(width * height * 4);
  let previous = new Uint8Array(stride);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)];
    const line = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    const row = new Uint8Array(stride);
    for (let i = 0; i < stride; i++) {
      const a = i >= channels ? row[i - channels] : 0, b = previous[i], c = i >= channels ? previous[i - channels] : 0;
      let p = 0;
      if (filter === 1) p = a;
      else if (filter === 2) p = b;
      else if (filter === 3) p = (a + b) >> 1;
      else if (filter === 4) { const e = a + b - c, pa = Math.abs(e - a), pb = Math.abs(e - b), pc = Math.abs(e - c); p = pa <= pb && pa <= pc ? a : pb <= pc ? b : c; }
      else if (filter !== 0) throw new Error(`bad PNG filter ${filter}`);
      row[i] = (line[i] + p) & 255;
    }
    for (let x = 0; x < width; x++) {
      const o = (y * width + x) * 4, s = x * channels;
      if (color === 3) { const k = row[s]; out.set([palette[k * 3], palette[k * 3 + 1], palette[k * 3 + 2], alpha && k < alpha.length ? alpha[k] : 255], o); }
      else if (channels <= 2) { out[o] = out[o + 1] = out[o + 2] = row[s]; out[o + 3] = channels === 2 ? row[s + 1] : 255; }
      else { out[o] = row[s]; out[o + 1] = row[s + 1]; out[o + 2] = row[s + 2]; out[o + 3] = channels === 4 ? row[s + 3] : 255; }
    }
    previous = row;
  }
  return { width, height, data: out };
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
const crc = (bytes) => { let c = 0xffffffff; for (const byte of bytes) c = CRC_TABLE[(c ^ byte) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
const chunk = (type, body) => {
  const head = Buffer.alloc(8); head.writeUInt32BE(body.length, 0); head.write(type, 4, "latin1");
  const tail = Buffer.alloc(4); tail.writeUInt32BE(crc(Buffer.concat([head.subarray(4), body])), 0);
  return Buffer.concat([head, body, tail]);
};

// encodePNG writes RGBA from { width, height, data } (data RGBA), or grey+alpha
// when { grey: true } and data holds two bytes a pixel. Filter 0 on every row
// and zlib level 9: the bytes depend on the pixels only.
export function encodePNG({ width, height, data, grey = false }) {
  const channels = grey ? 2 : 4;
  const header = Buffer.alloc(13); header.writeUInt32BE(width, 0); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = grey ? 4 : 6;
  const stride = width * channels, raw = Buffer.alloc(height * (stride + 1));
  for (let y = 0; y < height; y++) Buffer.from(data.buffer, data.byteOffset + y * stride, stride).copy(raw, y * (stride + 1) + 1);
  return Buffer.concat([SIGNATURE, chunk("IHDR", header), chunk("IDAT", deflateSync(raw, { level: 9 })), chunk("IEND", Buffer.alloc(0))]);
}
