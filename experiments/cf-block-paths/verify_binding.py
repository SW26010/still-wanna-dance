"""Verify the selected Wi-Fi adapter and export counts without local identities.

Promoted from the earlier Wi-Fi experiment's local checker; accepts that recorder's
unchanged private file format. It performs no network requests or system changes.
"""
import hashlib
import ipaddress
import json
import sys
from collections import Counter
from pathlib import Path

def endpoint(text):
    host, port = text.rsplit(':', 1)
    return ipaddress.ip_address(host.strip('[]')), int(port)

def check(path):
    private = json.loads((path/'network-private.json').read_bytes())
    assert private['Failure'] == '', private['Failure']
    a = private['Adapter']
    assert a['Name'] == 'Wi-Fi' and a['Up']
    allowed = {ipaddress.ip_address(s) for s in a['Addresses']}
    rows = [json.loads(s) for s in (path/'connections-private.jsonl').read_text().splitlines()]
    counts = Counter()
    other_errors = Counter()
    for r in rows:
        family = endpoint(r['Target'])[0].version
        index = a['Index4' if family == 4 else 'Index6']
        if r['InterfaceIndex']:
            assert r['InterfaceIndex'] == index
        if not r['Error']:
            assert r['InterfaceIndex'] == index
            assert endpoint(r['Local'])[0] in allowed
            assert endpoint(r['Remote']) == endpoint(r['Target'])
            counts[f'v{family}Connected'] += 1
        elif 'canceled' in r['Error']:
            counts['canceled'] += 1
        else:
            counts['otherFailed'] += 1
            # Keep error detail only in original private log.
            other_errors['timeout' if 'timeout' in r['Error'] or 'timed out' in r['Error'] else 'other'] += 1
    out = {'batch': path.name, 'tcpAttempts': len(rows), 'counts': dict(counts),
           'otherErrors': dict(other_errors), 'allConnectedSourcesOnSelectedWifi': True,
           'allConnectedPeersMatchTargets': True, 'allSelectedIndexesMatchAdapter': True,
           'fatalBindingError': False,
           'hashBasis': 'actual-file-bytes',
           'files': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(path.iterdir()) if p.is_file()}}
    print(json.dumps(out, indent=2))
    return out

if __name__ == '__main__':
    p = Path(sys.argv[1])
    result = check(p)
    if len(sys.argv) > 2:
        dst = Path(sys.argv[2]); dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8', newline='\n')
