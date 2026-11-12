"""Render category artwork from hash-checked originals or lossless masters.

Requires Pillow. Inputs live in the separate category-cover source archive;
ordinary application builds use the committed WebP files without this script.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path

from PIL import Image, ImageEnhance, ImageFilter, ImageOps, features
import PIL


def load_source(root, asset):
    path = (root / asset['encodingInputFile']).resolve()
    if not path.is_relative_to(root):
        raise ValueError(f"Input leaves source directory: {asset['asset']}")
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != asset['encodingInputSHA256']:
        raise ValueError(f"Input hash mismatch: {asset['asset']}")
    with Image.open(io.BytesIO(data)) as image:
        image.load()
        if 'A' in image.getbands() and image.getchannel('A').getextrema() != (255, 255):
            raise ValueError(f"Transparent source needs an explicit composition: {asset['asset']}")
        return image.convert('RGB')


def compose(source, asset, manifest):
    size = tuple(manifest['encoding']['pixels'])
    if asset['layout'] == 'official-wide-center-crop':
        return source.crop(tuple(asset['source']['cropBox'])).resize(size, Image.Resampling.LANCZOS)
    if asset['layout'] != 'whole-square-on-blurred-dim-background':
        raise ValueError(f"Unknown layout: {asset['layout']}")
    if source.width != source.height:
        raise ValueError(f"Square source expected: {asset['asset']}")
    layout = manifest['squareLayout']
    background = ImageOps.fit(source, size, Image.Resampling.LANCZOS)
    background = background.filter(ImageFilter.GaussianBlur(layout['blurRadiusPixels']))
    background = ImageEnhance.Brightness(background).enhance(layout['brightness'])
    foreground = ImageOps.contain(source, size, Image.Resampling.LANCZOS)
    background.paste(foreground, ((size[0] - foreground.width) // 2, (size[1] - foreground.height) // 2))
    return background


def encode(image, *, quality, method, lossless):
    buffer = io.BytesIO()
    image.save(buffer, format='WEBP', quality=quality, method=method, lossless=lossless)
    payload = buffer.getvalue()
    if lossless:
        with Image.open(io.BytesIO(payload)) as decoded:
            if decoded.convert('RGB').tobytes() != image.tobytes():
                raise ValueError('Lossless output differs from the composed RGB pixels')
    return payload


def main():
    repo = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sources', type=Path, required=True, help='Expanded source archive directory')
    parser.add_argument('--manifest', type=Path, default=repo / 'docs/research/category-cover-assets-20261112.json')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--check', action='store_true', help='Reproduce and compare all committed image bytes')
    mode.add_argument('--output', type=Path, help='Export images and a report to a staging directory')
    parser.add_argument('--quality', type=int, help='Experimental export quality, 0–100; omitted uses the manifest')
    parser.add_argument('--lossless', action='store_true', help='Experimental lossless export')
    args = parser.parse_args()
    if args.check and (args.quality is not None or args.lossless):
        parser.error('--check uses only the recorded production encoding')
    if args.quality is not None and not 0 <= args.quality <= 100:
        parser.error('--quality must be between 0 and 100')
    manifest = json.loads(args.manifest.read_text('utf-8'))
    encoding = manifest['encoding']
    quality = args.quality if args.quality is not None else encoding['quality']
    lossless = args.lossless or encoding['lossless']
    source_root = args.sources.resolve()
    output = args.output.resolve() if args.output else None
    if output:
        # Experiments never overwrite a source archive or registered production art.
        production = (repo / 'internal/console/assets/webui/category-covers').resolve()
        if output.is_relative_to(source_root) or source_root.is_relative_to(output) or output.is_relative_to(production):
            parser.error('Use a separate staging directory for --output')
        output.mkdir(parents=True, exist_ok=True)
    rows = []
    for asset in manifest['assets']:
        frame = compose(load_source(source_root, asset), asset, manifest)
        payload = encode(frame, quality=quality, method=encoding['method'], lossless=lossless)
        digest = hashlib.sha256(payload).hexdigest()
        if args.check:
            destination = (repo / asset['file']).resolve()
            if digest != asset['sha256'] or destination.read_bytes() != payload:
                raise ValueError(f"Output differs: {asset['asset']}; compare recorded Pillow/libwebp versions")
        else:
            (output / (asset['asset'] + '.webp')).write_bytes(payload)
        rows.append({'asset': asset['asset'], 'bytes': len(payload), 'sha256': digest})
    result = {'checked': args.check, 'assets': len(rows), 'totalBytes': sum(row['bytes'] for row in rows),
              'encoding': {**encoding, 'quality': quality, 'lossless': lossless},
              'losslessPixelEqualityVerified': lossless,
              'tools': {'Pillow': PIL.__version__, 'libwebp': features.version('webp')}, 'files': rows}
    if output:
        (output / 'encoding-report.json').write_text(json.dumps(result, indent=2) + '\n', 'utf-8')
    print(json.dumps({key: value for key, value in result.items() if key != 'files'}))


if __name__ == '__main__':
    main()
