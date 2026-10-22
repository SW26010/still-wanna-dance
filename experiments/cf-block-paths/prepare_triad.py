"""Freeze factual v1427 candidates and two independently sampled CF controls.

No network probes. The private decompiler file is input only; no code is copied.
"""
import argparse
import hashlib
import ipaddress
import json
import random
import re
from collections import Counter
from pathlib import Path

PREFIXES = '''173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22
141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20
197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13
104.24.0.0/14 172.64.0.0/13 131.0.72.0/22'''.split()
SEED = 202610220421


def prepare(source):
    raw = source.read_bytes()
    section = raw.decode('utf-8-sig').split('CandidateIps =', 1)[1].split('};', 1)[0]
    original = re.findall(r'"(\d+\.\d+\.\d+\.\d+)"', section)
    assert len(original) == len(set(original)) == 104
    nets = [ipaddress.ip_network(p) for p in PREFIXES]
    assert all(sum(ipaddress.ip_address(ip) in p for p in nets) == 1 for ip in original)
    total = sum(p.num_addresses for p in nets)
    def from_offset(offset):
        for p in nets:
            if offset < p.num_addresses:
                return str(p.network_address + offset)
            offset -= p.num_addresses
        raise AssertionError('offset outside address space')
    # Sampling without replacement. No exclusion based on historical performance
    # or overlap with A. All addresses in the published ranges are in scope.
    uniform = [from_offset(i) for i in random.Random(SEED).sample(range(total), 104)]
    strat_rng = random.Random(SEED + 1)
    order = list(range(15))
    strat_rng.shuffle(order)
    stratified = []
    allocation = {}
    for rank, i in enumerate(order):
        n = 7 if rank < 14 else 6
        p = nets[i]
        allocation[str(p)] = n
        stratified.extend(str(p.network_address + j) for j in strat_rng.sample(range(p.num_addresses), n))
    pools = {'A': original, 'B_uniform': uniform, 'B_stratified': stratified}
    structure = {}
    for name, ips in pools.items():
        structure[name] = {
            'count': len(ips),
            'prefixCounts': dict(Counter(str(next(p for p in nets if ipaddress.ip_address(ip) in p)) for ip in ips)),
            'lastOctetCounts': dict(Counter(ip.rsplit('.', 1)[1] for ip in ips)),
            'unique24Prefixes': len({str(ipaddress.ip_network(ip + '/24', strict=False)) for ip in ips}),
        }
    return {'schema': 1, 'method': 'cf-triad-v1', 'seed': SEED,
            'officialSource': 'https://www.cloudflare.com/ips-v4', 'checkedDate': '2026-10-22',
            'prefixes': PREFIXES, 'addressSpace': total, 'stratifiedAllocation': allocation,
            'sourceSHA256': hashlib.sha256(raw).hexdigest(),
            'pools': pools, 'structure': structure,
            'pilot': list(dict.fromkeys(['172.64.144.1', '172.64.229.213', '104.16.0.1',
                                        '104.17.160.1', '104.17.96.1'] + uniform[:2] + stratified[:2]))}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('source', type=Path)
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    result = prepare(args.source)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('x', encoding='utf-8', newline='\n') as f:
        json.dump(result, f, indent=2)
        f.write('\n')
    print(json.dumps({'addressSpace': result['addressSpace'], 'structure': result['structure']}, indent=2))
