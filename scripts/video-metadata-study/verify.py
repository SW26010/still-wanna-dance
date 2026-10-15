"""Check the frozen, redacted study without runtime storage or network access."""
import argparse
import ast
import csv
import hashlib
import json
import re
from pathlib import Path

import numpy as np

from analyze import stats


ROOT = Path(__file__).resolve().parents[2]
DATA = ROOT / 'docs/research/video-metadata-20261015'
REPORT = DATA.with_suffix('.md')


def load(name):
    return json.loads((DATA / name).read_text(encoding='utf-8'))


def read_csv(name):
    with (DATA / name).open(encoding='utf-8', newline='') as fp:
        return list(csv.DictReader(fp))


def near(actual, expected):
    if isinstance(expected, dict):
        return actual.keys() == expected.keys() and all(near(actual[k], v) for k, v in expected.items())
    if isinstance(expected, list):
        return len(actual) == len(expected) and all(near(a, b) for a, b in zip(actual, expected))
    if isinstance(expected, (int, float)):
        return bool(np.isclose(actual, expected, rtol=1e-10, atol=1e-8))
    return actual == expected


def published_files():
    return sorted([REPORT, *(p for p in DATA.iterdir() if p.name != 'manifest.json'),
                   *Path(__file__).parent.glob('*.py'), Path(__file__).with_name('requirements.txt')])


def fingerprint(path):
    content = path.read_bytes().replace(b'\r\n', b'\n')
    return {'path': path.relative_to(ROOT).as_posix(), 'bytes': len(content),
            'sha256': hashlib.sha256(content).hexdigest()}


def run(refresh):
    summary = load('summary.json')
    rows = read_csv('measurements.csv')
    by_number = {int(r['resource_no']): r for r in rows}
    columns = {k: np.array([float(r[k]) for r in rows])
               for k in ['duration_seconds', 'overall_mbps', 'size_mib', 'byte_size', 'moov_bytes']}
    duration, rate, size = (columns[k] for k in ['duration_seconds', 'overall_mbps', 'size_mib'])
    checks = {}
    checks['measurement_count'] = len(rows) == summary['local_files'] == summary['parsed_files']
    checks['unique_resources'] = len(by_number) == len(rows)
    checks['size_matches'] = (all(r['db_size_matches'] == 'True' for r in rows)
                              and summary['db_size_matches'] == len(rows)
                              and near(size.tolist(), (columns['byte_size'] / 2**20).tolist())
                              and near(summary['total_gib'], columns['byte_size'].sum() / 2**30))
    checks['resource_coverage'] = (
        sum(int(r['song_count']) > 0 for r in rows) == summary['current_local_resources']
        and sum(int(r['song_count']) == 0 for r in rows) == summary['unreferenced_local_resources']
        and sum(r['moov_before_mdat'] == 'True' for r in rows) == summary['moov_before_mdat'])
    checks['rate_formula'] = near(rate.tolist(), (columns['byte_size'] * 8 / duration / 1e6).tolist())
    checks['stats_recomputed'] = all(near(summary[k], stats(columns[k]))
                                     for k in ['duration_seconds', 'overall_mbps', 'size_mib', 'moov_bytes'])
    checks['histograms_include_all_files'] = all(
        sum(b['counts']) == len(rows) and np.array_equal(np.histogram(columns[k], b['edges'])[0], b['counts'])
        for k, b in load('histogram-bins.json').items())
    old = read_csv('catalog-comparison.csv')
    errors = [float(r['end_minus_duration']) for r in old]
    checks['local_catalog_comparison'] = (
        len(old) == summary['matched_songs'] and len({r['song_id'] for r in old}) == len(old)
        and all(near(float(r['duration_seconds']), float(by_number[int(r['resource_no'])]['duration_seconds']))
                and near(float(r['end']) - float(r['duration_seconds']), float(r['end_minus_duration'])) for r in old)
        and near(summary['end_minus_duration'], stats(errors))
        and near(summary['end_abs_error'], stats(np.abs(errors)))
        and all(sum(abs(e) <= float(t) for e in errors) == n for t, n in summary['end_error_within'].items()))
    current = read_csv('catalog-comparison-current.csv')
    current_errors = [abs(float(r['end']) - float(r['duration_seconds'])) for r in current]
    current_rate_errors = [abs(float(r['duration_seconds']) / float(r['end']) - 1) * 100 for r in current]
    catalogs = load('catalog-fields.json')
    checks['current_catalog_comparison'] = (
        catalogs['kiva']['revision'] == catalogs['udon']['revision']
        and len({r['song_id'] for r in current}) == len(current)
        and all(near(float(r['duration_seconds']), float(by_number[int(r['resource_no'])]['duration_seconds']))
                and near(float(r['abs_error']), e) and near(float(r['bitrate_relative_error_percent']), b)
                for r, e, b in zip(current, current_errors, current_rate_errors))
        and all(len(current) == c['matched'] and near(c['end_abs_error'], stats(current_errors))
                and near(c['estimated_bitrate_relative_error_percent'], stats(current_rate_errors))
                and c['within_one_second'] == sum(e <= 1 for e in current_errors) for c in catalogs.values()))
    fallback = {}
    for k, values, default in [('duration_seconds', duration, 210), ('overall_mbps', rate, 1.8), ('size_mib', size, 45)]:
        error = np.abs(values - default)
        fallback[k] = {'default': default, 'mean_absolute_error': float(error.mean()),
                       'p90_absolute_error': float(np.percentile(error, 90)),
                       'fraction_at_or_below_default': float((values <= default).mean())}
    fallback.update(fraction_at_or_below_3_mbps=float((rate <= 3).mean()),
                    fraction_at_or_below_80_mib=float((size <= 80).mean()),
                    fraction_at_or_below_285_seconds=float((duration <= 285).mean()),
                    consistent_triple_size_mib=210 * 1.8e6 / 8 / 2**20,
                    size_duration_correlation=float(np.corrcoef(size, duration)[0, 1]))
    checks['fallback_recomputed'] = near(load('fallback-analysis.json'), fallback)
    probes = load('http-probes.json')
    checks['remote_crosschecks'] = len(probes['probes']) == probes['successes'] == 13 and all(
        p['head']['status'] == 200 and int(p['head']['content_length']) == p['byte_size']
        and p['byte_size'] == int(by_number[p['resource_no']]['byte_size'])
        and p['duration_matches_local'] and p['local_full_md5_matches_url_e']
        and near(p['remote_metadata']['duration_seconds'], float(by_number[p['resource_no']]['duration_seconds']))
        and all(r['matches_local'] and r['status'] == 206 and r['content_range_total'] == p['byte_size'] for r in p['ranges'])
        for p in probes['probes'])
    checks['remote_byte_budget'] = probes['body_bytes_read'] == 2**20 == sum(p['body_bytes_read'] for p in probes['probes']) and all(
        p['body_bytes_read'] == sum(r['bytes'] for r in p['ranges']) <= 256 * 1024 for p in probes['probes'])
    failed = load('http-probes-default-ua.json')
    checks['failed_probe_evidence_retained'] = len(failed['probes']) == 13 and failed['body_bytes_read'] == failed['successes'] == 0 and all(
        p['head']['error'] == 'HTTPError' and '403' in p['range_error'] for p in failed['probes'])
    independent = load('independent-validation.json')
    checks['independent_validation'] = (
        len(independent) == summary['independent_validation']['n'] == 89
        and all(near(r['mvhd_duration_seconds'], float(by_number[r['resource_no']]['duration_seconds']))
                and near(r['av_duration_seconds'] - r['mvhd_duration_seconds'], r['duration_delta']) for r in independent)
        and near(max(abs(r['duration_delta']) for r in independent), summary['independent_validation']['max_absolute_duration_delta'])
        and near(max(abs(r['av_bit_rate'] - r['formula_bit_rate']) for r in independent), summary['independent_validation']['max_absolute_bit_rate_delta']))
    public_text = '\n'.join(p.read_text(encoding='utf-8') for p in [REPORT, *DATA.iterdir()] if p.is_file())
    checks['no_private_resource_identifiers'] = not re.search(
        r'(?<![0-9a-fA-F])[0-9a-fA-F]{32}(?![0-9a-fA-F])|\b[A-Za-z]:[\\/]|[?&]e=[0-9a-fA-F]|usr_[0-9a-f-]+', public_text)
    links = re.findall(r'\]\(([^)]+)\)', REPORT.read_text(encoding='utf-8'))
    checks['report_local_links'] = all((REPORT.parent / link.split('#')[0]).exists() for link in links if not re.match(r'https?://', link))
    checks['script_syntax'] = all(ast.parse(p.read_text(encoding='utf-8')) for p in Path(__file__).parent.glob('*.py'))
    result = {'type': 'research data checks; not game acceptance', 'checks': checks, 'passed': all(checks.values())}
    if not result['passed']:
        raise SystemExit(json.dumps(result, indent=2))
    manifest = load('manifest.json')
    if refresh:
        (DATA / 'checks.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8', newline='\n')
        manifest['files'] = [fingerprint(p) for p in published_files()]
        (DATA / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8', newline='\n')
    else:
        if result != load('checks.json'):
            raise SystemExit('Recorded checks differ; review before --refresh-manifest')
        if manifest['files'] != [fingerprint(p) for p in published_files()]:
            raise SystemExit('Manifest mismatch; review before --refresh-manifest')
    print(json.dumps({'passed': True, 'checks': len(checks), 'manifest_files': len(manifest['files'])}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--refresh-manifest', action='store_true', help='After checks pass, refresh checks.json and LF-normalized hashes')
    run(parser.parse_args().refresh_manifest)
