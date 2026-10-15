// Development-only: install sharp@0.35.5 locally (or provide it via NODE_PATH).
// Normal Go builds use the checked-in ICO and do not need Node or sharp.
import { createRequire } from 'node:module';
import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';

const sharp = createRequire(import.meta.url)('sharp');
const root = new URL('../internal/branding/', import.meta.url);
const source = await readFile(new URL('logo.svg', root));
const sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256];
const images = [];
for (const size of sizes) {
  // Render directly from the vector at each resolution, never upscale a small icon.
  const render = sharp(source).resize(size, size);
  if (size >= 128) {
    images.push(await render.png({ compressionLevel: 9 }).toBuffer());
    continue;
  }
  const rgba = await render.ensureAlpha().raw().toBuffer();
  const maskStride = Math.ceil(size / 32) * 4;
  const dib = Buffer.alloc(40 + size * size * 4 + maskStride * size);
  dib.writeUInt32LE(40, 0);
  dib.writeInt32LE(size, 4);
  dib.writeInt32LE(size * 2, 8); // XOR pixels followed by the AND mask.
  dib.writeUInt16LE(1, 12);
  dib.writeUInt16LE(32, 14);
  dib.writeUInt32LE(size * size * 4, 20);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const from = (y * size + x) * 4;
      const to = 40 + ((size - 1 - y) * size + x) * 4;
      dib[to] = rgba[from + 2];
      dib[to + 1] = rgba[from + 1];
      dib[to + 2] = rgba[from];
      dib[to + 3] = rgba[from + 3];
      if (rgba[from + 3] === 0) dib[40 + size * size * 4 + (size - 1 - y) * maskStride + (x >> 3)] |= 1 << (7 - x % 8);
    }
  }
  images.push(dib);
}
const directory = Buffer.alloc(6 + sizes.length * 16);
directory.writeUInt16LE(1, 2);
directory.writeUInt16LE(sizes.length, 4);
let offset = directory.length;
for (let i = 0; i < sizes.length; i++) {
  const entry = 6 + i * 16;
  directory[entry] = directory[entry + 1] = sizes[i] % 256;
  directory.writeUInt16LE(1, entry + 4);
  directory.writeUInt16LE(32, entry + 6);
  directory.writeUInt32LE(images[i].length, entry + 8);
  directory.writeUInt32LE(offset, entry + 12);
  offset += images[i].length;
}
const ico = Buffer.concat([directory, ...images]);
await writeFile(new URL('logo.ico', root), ico);
console.log(JSON.stringify({ sharp: sharp.versions.sharp, sizes, bytes: ico.length, sha256: createHash('sha256').update(ico).digest('hex') }));
