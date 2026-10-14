"""Download and verify a frozen evidence archive; Python standard library only."""
import argparse
import hashlib
import io
import json
import stat
import urllib.request
import zipfile
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[1]

def digest(data):
    return hashlib.sha256(data).hexdigest()

def verified_members(data, record):
    if len(data) != record['bytes'] or digest(data) != record['sha256']:
        raise ValueError('Archive size/SHA256 mismatch')
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        names = archive.namelist()
        if len(set(names)) != len(names):
            raise ValueError('Duplicate archive member')
        for info in archive.infolist():
            name = info.filename
            p = PurePosixPath(name)
            if (p.is_absolute() or '..' in p.parts or '\\' in name or ':' in name
                    or not name or name.endswith('/') or stat.S_ISLNK(info.external_attr >> 16)):
                raise ValueError('Unsafe archive member: ' + name)
        inventory_name = '_provenance/inventory.json'
        inventory = json.loads(archive.read(inventory_name))
        members = inventory['files']
        expected = {m['path'] for m in members}
        if len(expected) != len(members) or set(names) != expected | {inventory_name}:
            raise ValueError('Archive inventory does not cover exactly all members')
        result = {inventory_name: archive.read(inventory_name)}
        for item in members:
            raw = archive.read(item['path'])
            if len(raw) != item['bytes'] or digest(raw) != item['sha256']:
                raise ValueError('Member checksum mismatch: ' + item['path'])
            result[item['path']] = raw
        return result

def restore(data, record, destination):
    members = verified_members(data, record)
    destination = destination.resolve()
    # Check the entire destination before writing anything. Never overwrite
    # differing results or follow an existing symlink outside this destination.
    for name, raw in members.items():
        target = destination / name
        if not target.resolve().is_relative_to(destination):
            raise ValueError('Destination escapes archive root')
        if target.exists() and (not target.is_file() or target.read_bytes() != raw):
            raise ValueError('Existing destination differs: ' + name)
    for name, raw in members.items():
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if not target.exists():
            target.write_bytes(raw)
    return len(members)

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('batch', help='Batch directory name, or all')
    p.add_argument('--local-dir', type=Path, help='Use already downloaded ZIP files; no network')
    p.add_argument('--verify-only', action='store_true')
    args = p.parse_args()
    index = json.loads((ROOT/'docs/evidence/archives.json').read_text(encoding='utf-8'))
    records = [r for r in index['archives'] if args.batch in ('all', r['batch'])]
    if not records:
        p.error('Unknown batch')
    for record in records:
        if args.local_dir:
            data = (args.local_dir/record['name']).read_bytes()
        else:
            if not record['url'].startswith('https://github.com/SW26010/still-wanna-dance/releases/download/'):
                raise ValueError('Unexpected archive origin')
            with urllib.request.urlopen(record['url'], timeout=60) as response:
                data = response.read(record['bytes'] + 1)
        destination = ROOT/'artifacts/evidence'/record['batch']
        if args.verify_only:
            count = len(verified_members(data, record))
        else:
            count = restore(data, record, destination)
        print(f"{record['batch']}: verified {count} members" + ('' if args.verify_only else f'; {destination}'))

if __name__ == '__main__':
    main()
