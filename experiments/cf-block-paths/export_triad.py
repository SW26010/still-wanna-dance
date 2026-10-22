"""Create public aggregates and a local sanitized trace archive after sampling.

No publishing, network access, raw URL export, or modification of input evidence.
"""
import argparse
import hashlib
import json
import re
import shutil
import zipfile
from datetime import datetime
from pathlib import Path

from analyze_triad import analyze, holdout, public_error, public_sampling_period

PUBLICATION_REVISION = 2


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2)+'\n', encoding='utf-8', newline='\n')


def sanitize(data):
    start = datetime.fromisoformat(data['Started'])
    out = {k: v for k, v in data.items() if k not in ('Rows', 'Started', 'Finished', 'Error')}
    out['Error'] = public_error(data.get('Error', ''))
    out.update(public_sampling_period(data['Started']))
    out['timeBasis'] = 'milliseconds relative to this window start'
    out['Rows'] = []
    for row in data['Rows']:
        r = {k: v for k, v in row.items() if k not in ('ErrorDetail', 'Local', 'Location', 'ETag', 'Started', 'Ray')}
        r['StartMS'] = None if row['Started'].startswith('0001') else (datetime.fromisoformat(row['Started'])-start).total_seconds()*1000
        r['ETagSHA256'] = hashlib.sha256(row['ETag'].encode()).hexdigest() if row['ETag'] else ''
        r['Colo'] = row['Ray'].rsplit('-', 1)[-1] if row['Ray'] else ''
        out['Rows'].append(r)
    return out


def sanitize_freeze(data):
    """Keep evidence of ordering without publishing wall-clock times."""
    frozen = datetime.fromisoformat(data['frozenFileModifiedUTC'])
    first_end = datetime.fromisoformat(data['w1Finished'])
    second_start = datetime.fromisoformat(data['w2Started'])
    return {
        'schema': 2, **public_sampling_period(data['w1Finished']),
        'w1RawSHA256': data['w1RawSHA256'],
        'w1SelectionMatchesRecordedRanks': data['w1SelectionMatchesRecordedRanks'],
        'frozenBeforeW2': data['frozenBeforeW2'],
        'windowStartSeparationSeconds': data['windowStartSeparationSeconds'],
        'freezeAfterW1FinishedSeconds': round((frozen-first_end).total_seconds(), 3),
        'w2StartedAfterFreezeSeconds': round((second_start-frozen).total_seconds(), 3),
        'frozenBuildsMatch': data['frozenBuildsMatch'],
        'originalFrozenFilesSHA256': data['files'],
        'w2ReferenceMatchesHistorical': data['w2ReferenceMatchesHistorical'],
        'scope': data['scope'],
    }


def public_protocol(text):
    """Remove an internal task attribution, preserving the frozen method."""
    return re.sub(r'^本协议在[^\n]*?，方法 ', '本协议在采样前冻结，方法 ', text, flags=re.MULTILINE)


def archive_member(path, name):
    """ZIP metadata must not reintroduce local file or sampling times."""
    info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = 0o100644 << 16
    return info, path.read_bytes()


def export(root, destination):
    candidates_file = root/'candidates.json'
    candidates = json.loads(candidates_file.read_bytes())
    frozen = json.loads((root/'predictions-w1.json').read_bytes())
    archive_dir = root/f'sanitized-v{PUBLICATION_REVISION}'
    archive_dir.mkdir(exist_ok=False)
    destination.mkdir(parents=True, exist_ok=True)
    for n in (1, 2):
        path = root/f'window-{n:02}'/'triad-private.json'
        data = json.loads(path.read_bytes())
        assert data['Outcome'] != 'running', 'never export running data as final evidence'
        assert data['CandidatesSHA256'] == digest(candidates_file)
        summary = analyze(data, candidates)
        summary['rawSHA256'] = digest(path)
        if n == 2:
            summary['holdout'] = holdout(frozen, data)
        checks = json.loads((root/f'window-{n:02}-checks.json').read_bytes())
        assert checks['rawSHA256'] == summary['rawSHA256'] and checks['outcome'] == data['Outcome']
        if n == 1:
            assert frozen['rawSHA256'] == summary['rawSHA256']
        write(destination/f'window-{n:02}-summary.json', summary)
        write(destination/f'window-{n:02}-checks.json', checks)
        write(archive_dir/f'window-{n:02}-rows.json', sanitize(data))
        binding = root/f'window-{n:02}-binding.json'
        binding_data = json.loads(binding.read_bytes())
        assert binding_data['files']['triad-private.json'] == summary['rawSHA256']
        write(destination/binding.name, binding_data)
    shutil.copyfile(candidates_file, destination/'candidates.json')
    write(destination/'predictions-w1.json', frozen)
    write(destination/'freeze-validation.json', sanitize_freeze(json.loads((root/'freeze-validation.json').read_bytes())))
    for name in ('independent-content.json', 'final-verification.json'):
        write(destination/name, json.loads((root/name).read_bytes()))
    for name in ('protocol-v1.md', 'protocol-v2.md'):
        (archive_dir/name).write_text(public_protocol((root/name).read_text(encoding='utf-8')), encoding='utf-8', newline='\n')
    for name in ('source-v1.txt', 'source-v2.txt'):
        shutil.copyfile(root/name, archive_dir/name)
    for name in ('analyze_triad.py', 'prepare_triad.py', 'export_triad.py', 'verify_triad.py', 'verify_binding.py', 'plot_triad.py', 'test_analyze_triad.py', 'test_prepare_triad.py'):
        shutil.copyfile(Path(__file__).with_name(name), archive_dir/name)
    # Copy only this export's evidence, never stale retention or operation logs.
    public_json = [f'window-{n:02}-{kind}.json' for n in (1,2) for kind in ('summary','checks','binding')]
    public_json += ['candidates.json','predictions-w1.json','freeze-validation.json','independent-content.json','final-verification.json']
    for name in public_json:
        shutil.copyfile(destination/name, archive_dir/name)
    provenance_dir = archive_dir/'_provenance'
    provenance_dir.mkdir()
    shutil.copyfile(root/'base-source.zip', provenance_dir/'base-source.zip')
    shutil.copyfile(Path('internal/console/cf_triad_harness_test.go'), provenance_dir/'cf_triad_harness_test.go')
    provenance = {'baseHEAD': (root/'base-head.txt').read_text(encoding='utf-8-sig').strip(),
                  'baseSourceSHA256': digest(root/'base-source.zip'),
                  'sourceOverlay': 'Use source-v1.txt or source-v2.txt as internal/console/cf_triad_experiment_test.go over base-source.zip.',
                  'buildFlags': ['CGO_ENABLED=0', '-tags=no_easytier,cf_block_experiment'],
                  'builds': {str(n): {name: digest(root/name) for name in (
                      f'triad-v{n}.exe', f'source-v{n}.txt', f'protocol-v{n}.md')} for n in (1,2)},
                  'originalFrozenPredictionsSHA256': digest(root/'predictions-w1.json')}
    provenance['publicProtocolSnapshots'] = {f'protocol-v{n}.md': {
        'originalSHA256': digest(root/f'protocol-v{n}.md'),
        'publicSHA256': digest(archive_dir/f'protocol-v{n}.md'),
        'redaction': 'Internal task attribution removed; experiment method unchanged.'} for n in (1,2)}
    provenance['buildsHashBasis'] = 'Original private frozen files; public protocol copies have separate hashes.'
    write(provenance_dir/'provenance.json', provenance)
    write(destination/'provenance.json', provenance)
    members = {p.relative_to(archive_dir).as_posix(): {'sha256': digest(p), 'bytes': p.stat().st_size}
               for p in sorted(archive_dir.rglob('*')) if p.is_file()}
    write(archive_dir/'manifest.json', {'schema': 1, 'hashBasis': 'actual-file-bytes', 'files': members})
    archive = root/f'cf-triad-20261022-v{PUBLICATION_REVISION}.zip'
    with zipfile.ZipFile(archive, 'x', compression=zipfile.ZIP_DEFLATED) as z:
        for p in sorted(archive_dir.rglob('*')):
            if p.is_file():
                z.writestr(*archive_member(p, p.relative_to(archive_dir).as_posix()))
    with zipfile.ZipFile(archive) as z:
        assert z.testzip() is None
        for name, record in members.items():
            assert hashlib.sha256(z.read(name)).hexdigest() == record['sha256']
    files = {p.name: {'sha256': digest(p), 'bytes': p.stat().st_size} for p in sorted(destination.iterdir()) if p.is_file() and p.name != 'manifest.json'}
    manifest = {'schema': 1, 'method': 'cf-triad-v1', 'hashBasis': 'actual-file-bytes (UTF-8/LF public JSON)',
                'publicationRevision': PUBLICATION_REVISION,
                'status': 'local-verified-not-published', 'files': files,
                'archive': {'name': archive.name, 'bytes': archive.stat().st_size, 'sha256': digest(archive), 'members': members},
                'privateEvidence': {p.relative_to(root).as_posix(): digest(p) for p in sorted(root.rglob('*')) if p.is_file() and (p.suffix in ('.exe', '.jsonl') or p.name in ('triad-private.json', 'network-private.json', 'targets-private.json', 'frozen-v1.json', 'frozen-v2.json'))},
                'limitations': ['One selected network; two windows are not a population of independent time samples.',
                                'W1 pilot per-IP order reversal defect retained; W2 pilot uses corrected v2.',
                                'V2 isolates late httptrace callbacks; ordinary DNS TCP timing is not connection-specific in either version.',
                                'Absolute sampling times, internal task attribution, local topology and backup operation details are excluded from public evidence.',
                                'Raw URLs, local addresses, GUID, Location, ETag and error details are excluded from public traces.',
                                'Archive publication status: unpublished.']}
    write(destination/'manifest.json', manifest)
    return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('root', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    result = export(args.root, args.destination)
    print(json.dumps(result['archive'] | {'members': len(result['archive']['members'])}))
