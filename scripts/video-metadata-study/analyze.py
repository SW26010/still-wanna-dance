"""Read-only study of a copied SWD database and local MP4 metadata.

Outputs anonymous measurements; private resource mappings stay in --private.
Requires numpy; optional PyAV independently checks a deterministic sample.
Use an offline, consistent snapshot without an outstanding WAL, not a live DB.
"""
import argparse
import collections
import csv
import hashlib
import json
import random
import sqlite3
import struct
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

import numpy as np


def movie_header(read_at, size):
    """Follow box lengths, skipping media payload. Supports v0/v1 mvhd."""
    pos, boxes = 0, []
    while pos + 8 <= size:
        h = read_at(pos, 16)
        length, kind = struct.unpack('>I4s', h[:8])
        header = 8
        if length == 1:
            length, header = struct.unpack('>Q', h[8:16])[0], 16
        elif length == 0:
            length = size - pos
        if length < header or pos + length > size:
            raise ValueError('Invalid top-level box size')
        boxes.append(kind.decode('ascii', errors='replace'))
        if kind == b'moov':
            child = pos + header
            while child + 8 <= pos + length:
                b = read_at(child, min(40, pos + length - child))
                n, typ = struct.unpack('>I4s', b[:8])
                ch = 8
                if n == 1:
                    n, ch = struct.unpack('>Q', b[8:16])[0], 16
                    b = read_at(child, min(48, pos + length - child))
                elif n == 0:
                    n = pos + length - child
                if n < ch or child + n > pos + length:
                    raise ValueError('Invalid moov child')
                if typ == b'mvhd':
                    version = b[ch]
                    if version == 0:
                        scale, ticks = struct.unpack('>II', b[ch+12:ch+20])
                    elif version == 1:
                        scale, ticks = struct.unpack('>IQ', b[ch+20:ch+32])
                    else:
                        raise ValueError('Unsupported mvhd version')
                    if not scale or ticks in (0, 0xffffffff, 0xffffffffffffffff):
                        raise ValueError('Unavailable duration')
                    return dict(duration_seconds=ticks / scale, timescale=scale,
                                moov_offset=pos, moov_bytes=length,
                                moov_before_mdat='mdat' not in boxes,
                                mvhd_version=version)
                child += n
            raise ValueError('No mvhd')
        pos += length
    raise ValueError('No moov')


def stats(values):
    a = np.asarray(values, dtype=float)
    if not len(a):
        return {'n': 0}
    return {'n': len(a), 'mean': float(a.mean()), 'std': float(a.std()),
            **{k: float(v) for k, v in zip(
                ['min', 'p01', 'p05', 'p10', 'p25', 'median', 'p75', 'p90', 'p95', 'p99', 'max'],
                np.percentile(a, [0, 1, 5, 10, 25, 50, 75, 90, 95, 99, 100]))}}


def run(args):
    out, private = Path(args.output), Path(args.private)
    out.mkdir(parents=True, exist_ok=True)
    private.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(Path(args.database).resolve().as_uri() + '?mode=ro&immutable=1', uri=True)
    db.row_factory = sqlite3.Row
    assert db.execute('PRAGMA quick_check').fetchone()[0] == 'ok'
    media = {r['md5']: dict(r) for r in db.execute('SELECT * FROM media')}
    songs = [dict(r) for r in db.execute('SELECT s.song_id,s.start,s.end,sm.md5 FROM songs s JOIN song_media sm USING(song_id)')]
    memberships = set(r[0] for r in db.execute('SELECT song_id FROM catalog_members'))
    refs = collections.Counter(s['md5'] for s in songs)
    files = sorted(Path(args.videos).glob('*.mp4'))
    rows, mapping, failures = [], {}, []
    for i, f in enumerate(files):
        key = f.stem.lower()
        row = {'resource_no': i+1, 'song_count': refs[key], 'byte_size': f.stat().st_size}
        try:
            with f.open('rb') as fp:
                def read_at(offset, count):
                    fp.seek(offset)
                    return fp.read(count)
                row.update(movie_header(read_at, row['byte_size']))
            row['overall_mbps'] = row['byte_size'] * 8 / row['duration_seconds'] / 1e6
            row['size_mib'] = row['byte_size'] / 2**20
            row['db_size_matches'] = media.get(key, {}).get('byte_size') == row['byte_size']
            rows.append(row)
            mapping[key] = row
        except Exception as e:
            failures.append({'resource_no': i+1, 'error': type(e).__name__})
        if (i+1) % 2000 == 0:
            print(f'Parsed {i+1}/{len(files)}', flush=True)
    with (out/'measurements.csv').open('w', newline='', encoding='utf-8') as fp:
        w = csv.DictWriter(fp, fieldnames=list(rows[0]), lineterminator='\n')
        w.writeheader()
        w.writerows(rows)
    comparisons = []
    for s in songs:
        if s['md5'] not in mapping:
            continue
        r = mapping[s['md5']]
        d = r['duration_seconds']
        comparisons.append({'song_id': s['song_id'], 'resource_no': r['resource_no'],
                            'start': s['start'], 'end': s['end'], 'duration_seconds': d,
                            'end_minus_duration': s['end']-d if s['end'] is not None else None,
                            'end_start_minus_duration': s['end']-(s['start'] or 0)-d if s['end'] is not None else None})
    with (out/'catalog-comparison.csv').open('w', newline='', encoding='utf-8') as fp:
        w = csv.DictWriter(fp, fieldnames=list(comparisons[0]), lineterminator='\n')
        w.writeheader()
        w.writerows(comparisons)
    url_rows = [dict(r) for r in db.execute('SELECT song_id,url,md5,byte_size FROM song_urls')]
    urlcheck = collections.Counter()
    for r in url_rows:
        q = parse_qs(urlsplit(r['url']).query)
        urlcheck['total'] += 1
        urlcheck['has_s_e'] += 's' in q and 'e' in q
        urlcheck['size_equals_db'] += int(q['s'][0]) == r['byte_size']
        urlcheck['e_equals_db_md5'] += q['e'][0].lower() == r['md5']
        if r['md5'] in mapping:
            urlcheck['local_comparable'] += 1
            urlcheck['size_equals_local'] += int(q['s'][0]) == mapping[r['md5']]['byte_size']
    errors = [r['end_minus_duration'] for r in comparisons if r['end_minus_duration'] is not None]
    abserrors = [abs(v) for v in errors]
    summary = {
        'date': '2026-10-15', 'database_sha256': hashlib.sha256(Path(args.database).read_bytes()).hexdigest(),
        'database_quick_check': 'ok', 'catalog_members': len(memberships), 'songs': len(songs),
        'media_rows': len(media), 'unique_current_media': len(refs),
        'local_files': len(files), 'parsed_files': len(rows), 'failures': failures,
        'current_local_resources': sum(r['song_count'] > 0 for r in rows),
        'unreferenced_local_resources': sum(r['song_count'] == 0 for r in rows),
        'matched_songs': len(comparisons), 'missing_songs': len(songs)-len(comparisons),
        'db_size_matches': sum(r['db_size_matches'] for r in rows),
        'total_gib': sum(r['byte_size'] for r in rows)/2**30,
        'duration_seconds': stats([r['duration_seconds'] for r in rows]),
        'overall_mbps': stats([r['overall_mbps'] for r in rows]),
        'size_mib': stats([r['size_mib'] for r in rows]),
        'moov_before_mdat': sum(r['moov_before_mdat'] for r in rows),
        'moov_bytes': stats([r['moov_bytes'] for r in rows]),
        'url_checks': dict(urlcheck),
        'end_minus_duration': stats(errors), 'end_abs_error': stats(abserrors),
        'end_error_within': {str(t): sum(v <= t for v in abserrors) for t in [0.1,0.5,1,2,5,10]},
        'positive_start_songs': [r for r in comparisons if (r['start'] or 0) > 0],
        'largest_end_errors': sorted(comparisons, key=lambda r: abs(r['end_minus_duration'] or 0), reverse=True)[:20],
        'catalog_revision': [dict(r) for r in db.execute('SELECT catalog_key,revision,source FROM catalog_state')],
    }
    # Deterministic independent demuxer check, including each metric's extremes.
    try:
        import av
        sample = random.Random(20261015).sample(list(mapping), min(64, len(mapping)))
        for field in ['duration_seconds','overall_mbps','byte_size']:
            ordered = sorted(mapping, key=lambda k: mapping[k][field])
            sample.extend(ordered[:3]+ordered[-3:])
        by_number = {r['resource_no']: k for k, r in mapping.items()}
        sample.extend(by_number[r['resource_no']] for r in comparisons
                      if abs(r['end_minus_duration'] or 0)>1 or (r['start'] or 0)>0)
        validations = []
        for key in dict.fromkeys(sample):
            with av.open(str(Path(args.videos)/(key+'.mp4'))) as container:
                duration = container.duration / av.time_base
                validations.append({'resource_no': mapping[key]['resource_no'],
                                    'av_duration_seconds': duration,
                                    'mvhd_duration_seconds': mapping[key]['duration_seconds'],
                                    'duration_delta': duration-mapping[key]['duration_seconds'],
                                    'av_bit_rate': container.bit_rate,
                                    'formula_bit_rate': mapping[key]['overall_mbps']*1e6,
                                    'video_stream_bitrates': [s.bit_rate for s in container.streams.video]})
        (out/'independent-validation.json').write_text(json.dumps(validations, indent=2), encoding='utf-8')
        summary['independent_validation'] = {'library': 'PyAV '+av.__version__, 'n': len(validations),
            'max_absolute_duration_delta': max(abs(r['duration_delta']) for r in validations),
            'max_absolute_bit_rate_delta': max(abs(r['av_bit_rate']-r['formula_bit_rate']) for r in validations)}
    except ImportError:
        summary['independent_validation'] = 'PyAV unavailable'
    (out/'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding='utf-8')
    (private/'resource-map.json').write_text(json.dumps(mapping), encoding='utf-8')
    (private/'urls.json').write_text(json.dumps(url_rows), encoding='utf-8')
    print(json.dumps(summary, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('--database', required=True)
    p.add_argument('--videos', required=True)
    p.add_argument('--output', required=True)
    p.add_argument('--private', required=True)
    run(p.parse_args())
