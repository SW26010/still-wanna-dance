"""Offline analysis of frozen cf-triad-v1 windows; never initiates probes.

Failures, screened-out candidates and genuinely missing observations are distinct.
The output contains aggregate public evidence, not URLs or local network identity.
"""
import argparse
import hashlib
import ipaddress
import json
import math
import random
import statistics
from collections import Counter, defaultdict
from datetime import datetime, timedelta, timezone
from pathlib import Path

METHODS = ('speed', 'http', 'https')
POOLS = ('A', 'B_uniform', 'B_stratified', 'C')


def public_sampling_period(started):
    """Publish a coarse local period, never an absolute sampling timestamp."""
    local = datetime.fromisoformat(started).astimezone(timezone(timedelta(hours=8)))
    period = ('morning' if 6 <= local.hour < 12 else
              'afternoon' if 12 <= local.hour < 18 else
              'evening' if 18 <= local.hour < 24 else 'night')
    return {'samplingDate': local.date().isoformat(), 'samplingPeriod': period,
            'samplingTimezone': 'Asia/Shanghai'}


def public_error(value):
    # Prepare embeds the underlying HTTP error, which can contain a resource URL.
    if not value:
        return ''
    if value.startswith(('prepare_1343_', 'prepare_1344_')):
        return '_'.join(value.split('_')[:2])
    if value in ('save_targets', 'dns_discovery', 'candidate_plan', 'window_deadline_or_network',
                 'body_budget', 'network_binding', 'critical_stop_status',
                 'critical_content_mismatch', 'critical_identity'):
        return value
    if value.startswith('reference_') and value[10:] in (
            'transport', 'redirect', 'status', 'headers', 'body_read', 'incomplete'):
        return value
    return 'experiment_failure'


def success(r):
    if not r or r['Error'] or r['Bytes'] != r['Requested'] or r['ElapsedMS'] <= 0:
        return False
    assert r['ContentLength'] == r['Requested'] and r['Protocol'] == 'HTTP/1.1'
    assert not r['Reused'] and (not r['IP'] or r['Remote'] == r['IP'])
    assert r['ContentEncoding'] in ('', 'identity')
    assert len(r['Hash']) == 64
    if r['Method'] != 'http':
        assert r['TLSVerified'] and r['SNI'] == r['Host']
    assert r['Status'] == (200 if r['Method'] == 'speed' else 206)
    return True


def rate(r):
    return r['Bytes'] / (1 << 20) * 1000 / r['ElapsedMS'] if success(r) else 0


def stats(values):
    return {'n': len(values), 'min': min(values), 'median': statistics.median(values),
            'max': max(values)} if values else {'n': 0}


def ranks(values):
    result = [None] * len(values)
    order = sorted(range(len(values)), key=values.__getitem__)
    i = 0
    while i < len(order):
        j = i + 1
        while j < len(order) and values[order[j]] == values[order[i]]:
            j += 1
        for k in order[i:j]:
            result[k] = (i + j - 1) / 2 + 1
        i = j
    return result


def spearman(x, y):
    assert len(x) == len(y)
    if len(x) < 3:
        return None
    a, b = ranks(x), ranks(y)
    ma, mb = statistics.mean(a), statistics.mean(b)
    cov = sum((v-ma)*(w-mb) for v, w in zip(a, b))
    denom = math.sqrt(sum((v-ma)**2 for v in a)*sum((w-mb)**2 for w in b))
    return cov/denom if denom else None


def wilson(k, n):
    if not n:
        return None
    z = 1.959963984540054
    p = k/n
    mid = (p + z*z/(2*n))/(1+z*z/n)
    half = z*math.sqrt(p*(1-p)/n+z*z/(4*n*n))/(1+z*z/n)
    return [max(0, mid-half), min(1, mid+half)]


def top(rows, k):
    return sorted((r for r in rows if success(r)), key=lambda r: (-rate(r), r['IP']))[:k]


def recommendation(speed, video):
    """Same-window prediction diagnostics, never a causal or holdout estimate."""
    s = {r['IP']: r for r in speed}
    v = {r['IP']: r for r in video}
    measured = [ip for ip in sorted(s.keys() & v.keys())
                if s[ip]['Error'] != 'screened_out' and v[ip]['Error'] != 'screened_out']
    both = [ip for ip in measured if success(s[ip]) and success(v[ip])]
    out = {'measuredPairs': len(measured), 'bothComplete': len(both),
           'spearmanCompleteOnly': spearman([rate(s[ip]) for ip in both], [rate(v[ip]) for ip in both]),
           'spearmanFailuresTiedLast': spearman([rate(s[ip]) for ip in measured], [rate(v[ip]) for ip in measured])}
    actual = top(video, 5)
    fast = top(speed, len(speed))
    best = rate(actual[0]) if actual else 0
    known = lambda ip: ip in v and v[ip]['Error'] != 'screened_out'
    out['speedSuccessVideoFailure'] = sum(known(r['IP']) and not success(v[r['IP']]) for r in fast)
    out['speedSuccessVideoUnknown'] = sum(not known(r['IP']) for r in fast)
    out['speedSuccessVideoBelowHalfBest'] = sum(success(v.get(r['IP'])) and rate(v[r['IP']]) < best/2 for r in fast)
    out['speedSuccessCount'] = len(fast)
    out['topK'] = {}
    for k in (1, 3, 5):
        chosen = fast[:k]
        hits = {r['IP'] for r in chosen} & {r['IP'] for r in actual}
        out['topK'][str(k)] = {'selected': [r['IP'] for r in chosen],
                              'hitsInActualTop5': len(hits),
                              'containsActualBest': bool(actual and actual[0]['IP'] in {r['IP'] for r in chosen}),
                              'unknown': sum(not known(r['IP']) for r in chosen),
                              'bestSelectedObservedRatioToActualBest': max((rate(v.get(r['IP'])) for r in chosen), default=0)/best if best else None}
    out['selectedTop1RatioToActualBest'] = rate(v.get(fast[0]['IP']))/best if fast and best and known(fast[0]['IP']) else None
    out['actualBestIP'] = actual[0]['IP'] if actual else None
    return out


def protocol_pairs(rows):
    pairs = defaultdict(dict)
    for r in rows:
        if r['Method'] in ('http', 'https'):
            pairs[(r['Pool'], r['IP'], r['Round'], r['Song'])][r['Method']] = r
    counts = Counter()
    ratios, first, body = [], [], []
    by_ip = defaultdict(list)
    for key, pair in pairs.items():
        h, t = pair.get('http'), pair.get('https')
        if h is None or t is None:
            counts['missingPair'] += 1
        elif h['Error'] == 'screened_out' or t['Error'] == 'screened_out':
            counts['screenedOut'] += 1
        elif success(h) and success(t):
            counts['bothComplete'] += 1
            ratio = t['ElapsedMS']/h['ElapsedMS']
            ratios.append(ratio)
            first.append(t['FirstByteMS']-h['FirstByteMS'])
            body.append(t['BodyMS']-h['BodyMS'])
            by_ip[key[1]].append(ratio)
        elif success(h):
            counts['httpOnly'] += 1
        elif success(t):
            counts['httpsOnly'] += 1
        else:
            counts['bothFailed'] += 1
    return {'counts': dict(counts), 'httpRateOverHttpsRate': stats(ratios),
            'httpsMinusHttpFirstByteMS': stats(first), 'httpsMinusHttpBodyMS': stats(body),
            'httpFasterPairs': sum(r > 1 for r in ratios),
            'httpAtLeastTwicePairs': sum(r >= 2 for r in ratios),
            'perIPMedianRatio': {ip: statistics.median(v) for ip, v in by_ip.items()}}


def stratified_interval(ips, rows, prefixes):
    nets = [ipaddress.ip_network(p) for p in prefixes]
    data = defaultdict(list)
    lookup = {r['IP']: r for r in rows}
    for ip in ips:
        if ip not in lookup:
            return {'complete': False}
        p = str(next(p for p in nets if ipaddress.ip_address(ip) in p))
        data[p].append(int(success(lookup[ip])))
    weights = {str(p): p.num_addresses/sum(q.num_addresses for q in nets) for p in nets}
    estimate = sum(weights[p]*statistics.mean(data[p]) for p in data)
    rng = random.Random(202610220422)
    boot = sorted(sum(weights[p]*statistics.mean(rng.choices(data[p], k=len(data[p]))) for p in data) for _ in range(4000))
    return {'complete': True, 'addressWeightedEstimate': estimate,
            'conditionalStratifiedBootstrap95': [boot[99], boot[3899]],
            'strata': {p: {'n': len(v), 'success': sum(v)} for p, v in data.items()},
            'limitation': 'Conditional on one observed window and sampled strata; all-zero/all-one small strata can give overconfident bootstrap bounds.'}


def analyze(data, candidates):
    assert data['Method'] == 'cf-triad-v1' and data['Schema'] == 1
    rows = data['Rows'] or []
    seen = set()
    hashes = defaultdict(set)
    for r in rows:
        key = tuple(r[k] for k in ('Stage', 'Pool', 'IP', 'Song', 'Round', 'Method'))
        assert key not in seen, f'duplicate row {key}'
        seen.add(key)
        if success(r) and r['Method'] != 'speed':
            hashes[(r['Song'], r['Requested'])].add(r['Hash'])
    assert all(len(v) == 1 for v in hashes.values()), 'successful video content differs'
    # Prepare performs one validated byte request per resource before reference.
    prep_bytes = data['BodyBytes'] - sum(r['Bytes'] for r in rows)
    expected_prep = 2 if data['Window'] == 2 else 1
    assert 0 <= prep_bytes <= expected_prep
    if data['Candidates']:
        assert prep_bytes == expected_prep
    out = {'schema': 1, 'method': data['Method'], 'window': data['Window'],
           'outcome': data['Outcome'], 'error': public_error(data['Error']),
           **public_sampling_period(data['Started']),
           'bodyBytes': data['BodyBytes'], 'recordedRows': len(rows),
           'candidatesSHA256': data['CandidatesSHA256'],
           'contentHashes': {f'{k[0]}:{k[1]}': next(iter(v)) for k, v in hashes.items()},
           'pools': {}, 'pilot': protocol_pairs([r for r in rows if r['Stage'] == 'pilot'])}
    if data['Finished'] and not data['Finished'].startswith('0001'):
        out['wallSeconds'] = (datetime.fromisoformat(data['Finished'])-datetime.fromisoformat(data['Started'])).total_seconds()
    for pool in POOLS:
        ips = [c['IP'] for c in data['Candidates'] if c['Pool'] == pool]
        full = [r for r in rows if r['Stage'] == 'measure' and r['Pool'] == pool]
        screens = [r for r in rows if r['Stage'] == 'screen' and r['Pool'] == pool]
        info = {'plannedCandidates': len(ips), 'methods': {}, 'protocolPairs': protocol_pairs(full),
                'measuredBodyBytes': sum(r['Bytes'] for r in full),
                'screenBodyBytes': sum(r['Bytes'] for r in screens),
                'requestTimeMS': sum(r['ElapsedMS'] for r in full + screens)}
        screen_by_ip = defaultdict(list)
        for r in screens:
            screen_by_ip[r['IP']].append(r)
        eliminated = {ip for ip, rs in screen_by_ip.items() if len(rs) == 3 and not any(success(r) for r in rs)}
        audited = [r for r in full if r['IP'] in eliminated and r['Error'] != 'screened_out']
        info['screenAudit'] = {'allThreeScreenFailed': len(eliminated),
                               'auditedIPs': len({r['IP'] for r in audited}),
                               'fullSuccessByMethod': {m: sum(success(r) for r in audited if r['Method'] == m) for m in METHODS}}
        for method in METHODS:
            items = [r for r in full if r['Method'] == method]
            scr = [r for r in screens if r['Method'] == method]
            valid = [r for r in items if success(r)]
            screen_ok = sum(success(r) for r in scr)
            missing = len(ips)-len(items)
            screened = sum(r['Error'] == 'screened_out' for r in items)
            item = {'screenCount': len(scr), 'screenSuccess': screen_ok, 'fullSuccess': len(valid),
                    'fullAttempted': len(items)-screened, 'screenedOut': screened, 'missing': missing,
                    'fullSuccessFractionBounds': [len(valid)/len(ips), (len(valid)+screened+missing)/len(ips)] if ips else None,
                    'errors': dict(Counter(r['Error'] for r in items if r['Error'])),
                    'screenRequestTimeMS': sum(r['ElapsedMS'] for r in scr),
                    'fullRequestTimeMS': sum(r['ElapsedMS'] for r in items),
                    'readBodyBytes': sum(r['Bytes'] for r in items+scr),
                    'timeoutRequests': sum(any(term in r.get('ErrorDetail', '').lower() for term in ('deadline exceeded', 'timeout', 'timed out')) for r in items),
                    'partialFailedBytes': sum(r['Bytes'] for r in items if r['Error'] and r['Error'] != 'screened_out'),
                    'successMiBs': stats([rate(r) for r in valid]),
                    'firstByteMS': stats([r['FirstByteMS'] for r in valid]),
                    'bodyMS': stats([r['BodyMS'] for r in valid]),
                    'cache': dict(Counter(r['Cache'] for r in items if r['Status'])),
                    'colos': dict(Counter(r['Ray'].rsplit('-', 1)[-1] for r in items if r['Ray'])),
                    'top5': [{'ip': r['IP'], 'miBs': rate(r), 'colo': r['Ray'].rsplit('-', 1)[-1]} for r in top(items, 5)]}
            if pool == 'B_uniform' and len(scr) == len(ips):
                item['screenWilson95'] = wilson(screen_ok, len(scr))
            if pool == 'B_stratified':
                item['screenStratified'] = stratified_interval(ips, scr, candidates['prefixes'])
            info['methods'][method] = item
        info['prediction'] = {m: recommendation([r for r in full if r['Method'] == 'speed'], [r for r in full if r['Method'] == m]) for m in ('http', 'https')}
        out['pools'][pool] = info
    return out


def predictions(data):
    return {'method': 'cf-triad-v1', 'window': data['Window'],
            'picks': {pool: {method: [r['IP'] for r in top([r for r in data['Rows'] if r['Stage'] == 'measure' and r['Pool'] == pool and r['Method'] == method], 5)] for method in METHODS} for pool in POOLS}}


def holdout(frozen, later):
    assert frozen['window'] == 1 and later['Window'] == 2
    result = {}
    for pool, methods in frozen['picks'].items():
        result[pool] = {}
        for target in ('http', 'https'):
            rows = [r for r in later['Rows'] if r['Stage'] == 'measure' and r['Pool'] == pool and r['Method'] == target]
            lookup = {r['IP']: r for r in rows}
            best = top(rows, 1)
            result[pool][target] = {}
            for source, picks in methods.items():
                result[pool][target][source] = {'topK': {str(k): {
                    'ips': picks[:k], 'successful': sum(success(lookup.get(ip)) for ip in picks[:k]),
                    'unknown': sum(ip not in lookup or lookup[ip]['Error'] == 'screened_out' for ip in picks[:k]),
                    'bestSelectedObservedRatio': max((rate(lookup.get(ip)) for ip in picks[:k]), default=0)/rate(best[0]) if best else None,
                } for k in (1, 3, 5)}}
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('input', type=Path)
    parser.add_argument('--candidates', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--freeze', type=Path)
    parser.add_argument('--holdout', type=Path)
    args = parser.parse_args()
    raw = args.input.read_bytes()
    data = json.loads(raw)
    candidate_bytes = args.candidates.read_bytes()
    assert hashlib.sha256(candidate_bytes).hexdigest() == data['CandidatesSHA256']
    result = analyze(data, json.loads(candidate_bytes))
    result['rawSHA256'] = hashlib.sha256(raw).hexdigest()
    if args.freeze:
        assert data['Window'] == 1 and data['Outcome'] == 'complete'
        frozen = predictions(data)
        frozen['rawSHA256'] = result['rawSHA256']
        with args.freeze.open('x', encoding='utf-8') as f:
            json.dump(frozen, f, indent=2)
            f.write('\n')
    if args.holdout:
        result['holdout'] = holdout(json.loads(args.holdout.read_bytes()), data)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8', newline='\n')
    print(json.dumps({'outcome': result['outcome'], 'rows': len(data['Rows']), 'bodyMiB': result['bodyBytes']/(1 << 20)}))
