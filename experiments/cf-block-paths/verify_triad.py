"""Independent evidence invariants for cf-triad windows (read-only, no probes)."""
import argparse
import hashlib
import json
from collections import Counter, defaultdict
from datetime import datetime, timedelta
from pathlib import Path

from analyze_triad import success

ORDERS = (('speed','http','https'), ('speed','https','http'),
          ('http','https','speed'), ('https','http','speed'))


def verify_response(row, target):
    assert 0 <= row['Bytes'] <= row['Requested']+1
    if not success(row):
        return
    if row['Method'] == 'speed':
        assert row['Host'] == 'speed.cloudflare.com' and not row['Range']
    else:
        assert row['Host'] == target['Host']
        assert row['Range'] == f"bytes=0-{row['Requested']-1}"
        assert row['ContentRange'] == f"bytes 0-{row['Requested']-1}/{target['Size']}"
        assert row['ETag'] == target['ETag']
    assert 0 < row['FirstByteMS'] <= row['HeadersMS'] <= row['ElapsedMS']
    assert 0 <= row['BodyMS'] <= row['ElapsedMS']-row['HeadersMS']+1
    previous = 0
    for point in row['Progress'] or []:
        assert previous < point['Bytes'] <= row['Bytes']
        assert row['HeadersMS'] <= point['MS'] <= row['ElapsedMS']
        previous = point['Bytes']


def verify(directory, candidates, version):
    raw_path = directory/'triad-private.json'
    raw_bytes = raw_path.read_bytes()
    data = json.loads(raw_bytes)
    targets = json.loads((directory/'targets-private.json').read_bytes())
    frozen = json.loads(candidates.read_bytes())
    assert data['CandidatesSHA256'] == hashlib.sha256(candidates.read_bytes()).hexdigest()
    assert data['Window'] in (1,2) and data['Method'] == frozen['method'] == 'cf-triad-v1'
    for pool in ('A','B_uniform','B_stratified'):
        observed = [c['IP'] for c in data['Candidates'] if c['Pool']==pool]
        assert len(observed)==104 and Counter(observed)==Counter(frozen['pools'][pool])
    assert Counter(c['IP'] for c in data['Candidates'] if c['Pool']=='C') == Counter(['']+data['DNSIPs'])
    assert [c['IP'] for c in data['Pilot']] == frozen['pilot']+data['DNSIPs']+['']
    expected = []
    songs = [1343] if data['Window']==1 else [1343,1344]
    for song in songs:
        expected.append(('reference','reference','',song,0,0,'https',8<<20))
    pilot_orders = defaultdict(list)
    for round in range(2):
        for i in range(len(data['Pilot'])):
            pos = (i+round*7)%len(data['Pilot'])
            ip = data['Pilot'][pos]['IP']
            order = ORDERS[((i if version==1 else pos)+round+2*(data['Window']-1))%4]
            pilot_orders[ip].append(order.index('http') < order.index('https'))
            for method in order:
                expected.append(('pilot','pilot',ip,1343,round+1,i,method,(2 if method=='speed' else 8)<<20))
    if not data['PilotOnly']:
        for i, c in enumerate(data['Candidates']):
            for stage, length in (('screen',1024),('measure',2<<20)):
                for method in ORDERS[(i+2*(data['Window']-1))%4]:
                    expected.append((stage,c['Pool'],c['IP'],songs[-1],1,i,method,length))
    actual = [tuple(r[k] for k in ('Stage','Pool','IP','Song','Round','Position','Method','Requested')) for r in data['Rows']]
    assert actual == expected[:len(actual)], 'execution differs from frozen plan'
    if data['Outcome']=='complete':
        assert len(actual)==len(expected), 'complete label with missing planned rows'
    balance = all(len(v)==2 and v[0]!=v[1] for v in pilot_orders.values())
    if version==2:
        assert balance
    last_end = None
    screens = defaultdict(list)
    audit_counts = Counter()
    eligibility = {}
    for r in data['Rows']:
        verify_response(r, targets[str(r['Song'])])
        if r['Error']=='screened_out':
            assert r['Started'].startswith('0001') and r['Bytes']==0 and r['ElapsedMS']==0
        else:
            started = datetime.fromisoformat(r['Started'])
            if last_end:
                assert started >= last_end-timedelta(milliseconds=1), 'measurement overlap'
            last_end = started+timedelta(milliseconds=r['ElapsedMS'])
        key = r['Pool'],r['IP']
        if r['Stage']=='screen':
            screens[key].append(r)
        if r['Stage']=='measure':
            if key not in eligibility:
                assert len(screens[key])==3
                eligible=any(success(s) for s in screens[key])
                if not eligible and audit_counts[r['Pool']]<8:
                    eligible=True
                    audit_counts[r['Pool']]+=1
                eligibility[key]=eligible
            assert (r['Error']!='screened_out') == eligibility[key], 'unregistered screening selection'
    assert data['BodyBytes'] <= 3<<30
    if data['Outcome']=='complete':
        elapsed=(datetime.fromisoformat(data['Finished'])-datetime.fromisoformat(data['Started'])).total_seconds()
        assert elapsed <= 150*60+1
    return {'schema':1, 'window':data['Window'], 'recorderVersion':version,
            'outcome':data['Outcome'], 'recordedRows':len(actual),'plannedRows':len(expected),
            'planPrefixMatches':True,'completedPlan':len(actual)==len(expected),
            'responseAndProgressInvariants':True,'serialMeasurementConfirmed':True,
            'screeningAndAuditRuleConfirmed':True,'auditedByPool':dict(audit_counts),
            'pilotEachIPReversed':balance,
            'knownDeviation': 'W1 pilot order retained without per-IP reversal' if not balance else '',
            'rawSHA256':hashlib.sha256(raw_bytes).hexdigest()}


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('directory',type=Path)
    parser.add_argument('--candidates',type=Path,required=True)
    parser.add_argument('--version',type=int,choices=(1,2),required=True)
    parser.add_argument('--out',type=Path,required=True)
    args=parser.parse_args()
    result=verify(args.directory,args.candidates,args.version)
    args.out.write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8',newline='\n')
    print(json.dumps(result))
