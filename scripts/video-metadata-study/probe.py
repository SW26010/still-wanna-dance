"""Bounded HTTP metadata probes for the companion local video study.

Raw catalogues and URLs are read from the ignored private directory. No URLs,
resource hashes or file paths are included in published probe results.
"""
import argparse
import collections
import concurrent.futures
import csv
import hashlib
import json
import re
import sqlite3
import urllib.request
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlsplit, urlunsplit

import analyze as study


def run(args):
    private, out = Path(args.private), Path(args.output)
    out.mkdir(parents=True, exist_ok=True)
    mapping = json.loads((private/'resource-map.json').read_text(encoding='utf-8'))
    urls = json.loads((private/'urls.json').read_text(encoding='utf-8'))
    db = sqlite3.connect((private/'storage-snapshot.sqlite').resolve().as_uri()+'?mode=ro&immutable=1', uri=True)
    catalogue = {}
    latest_kiva = json.loads((private/'kiva-catalog.json').read_bytes())
    latest_entries = {e['id']: e for g in latest_kiva['data']['groups'] for e in g['entries']}
    for name in ['kiva','udon']:
        path = private/(name+'-catalog.json')
        body = path.read_bytes()
        raw = json.loads(body)
        if name == 'kiva':
            entries = [e for g in raw['data']['groups'] for e in g['entries']]
            revision = raw['data']['time']
        else:
            entries = [e for g in raw['groups']['contents'] for e in g['songInfos']]
            revision = raw['time']
        fields = collections.Counter(k for e in entries for k in e)
        comparisons = []
        dbmap = dict(db.execute('SELECT song_id,md5 FROM song_media'))
        for e in entries:
            # Udon and Kiva snapshots must describe the same revision and fields.
            if name == 'udon':
                assert revision == latest_kiva['data']['time']
                assert (e['start'], e['end']) == (latest_entries[e['id']]['start'], latest_entries[e['id']]['end'])
            key = e.get('checksum') if name == 'kiva' else latest_entries[e['id']]['checksum']
            if key in mapping and isinstance(e.get('end'), (float,int)):
                duration = mapping[key]['duration_seconds']
                comparisons.append({'song_id':e['id'],'resource_no':mapping[key]['resource_no'],
                    'end':e['end'],'duration_seconds':duration,'abs_error':abs(e['end']-duration),
                    'bitrate_relative_error_percent':abs(duration/e['end']-1)*100 if e['end']>0 else None})
        if name == 'kiva':
            with (out/'catalog-comparison-current.csv').open('w', newline='', encoding='utf-8') as fp:
                writer = csv.DictWriter(fp, fieldnames=list(comparisons[0]), lineterminator='\n')
                writer.writeheader()
                writer.writerows(comparisons)
        catalogue[name] = {'revision':revision,'bytes':len(body),'sha256':hashlib.sha256(body).hexdigest(),
            'song_entries':len(entries),'unique_ids':len(set(e['id'] for e in entries)),
            'field_counts':dict(fields),'matched':len(comparisons),
            'end_abs_error':study.stats([c['abs_error'] for c in comparisons]),
            'within_one_second':sum(c['abs_error']<=1 for c in comparisons),
            'estimated_bitrate_relative_error_percent':study.stats([c['bitrate_relative_error_percent'] for c in comparisons]),
            'changed_md5_since_local_catalog':sum(e['checksum'] != dbmap[e['id']] for e in latest_entries.values() if e['id'] in dbmap),
            'added_song_ids':sorted(set(latest_entries)-set(dbmap)),
            'removed_song_ids':sorted(set(dbmap)-set(latest_entries)),
            'mapping_basis':'catalog checksum' if name=='kiva' else 'same-revision Kiva checksum; start/end cross-checked by song ID'}
    (out/'catalog-fields.json').write_text(json.dumps(catalogue,ensure_ascii=False,indent=2),encoding='utf-8')
    if args.catalog_only:
        print(json.dumps(catalogue,ensure_ascii=False,indent=2))
        return
    # Cover size deciles plus local tail-moov resources with actual saved URL
    # evidence. Never invent an upstream address from resource metadata.
    eligible = list({r['md5']:r for r in urls if r['md5'] in mapping}.values())
    eligible.sort(key=lambda r:mapping[r['md5']]['byte_size'])
    decile_samples = [eligible[round((len(eligible)-1)*i/9)] for i in range(10)]
    tail = [r for r in eligible if not mapping[r['md5']]['moov_before_mdat']]
    tail_samples = [tail[0], tail[len(tail)//2], tail[-1]] if tail else []
    chosen = decile_samples + tail_samples

    def probe(item):
        key = item['md5']
        local = mapping[key]
        parts = urlsplit(item['url'])
        url = urlunsplit(('https',parts.netloc,parts.path,parts.query,''))
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        result = {'song_id':item['song_id'],'resource_no':local['resource_no'],
                  'byte_size':local['byte_size'],'local_duration_seconds':local['duration_seconds'],
                  'moov_before_mdat':local['moov_before_mdat'],'head':{},'ranges':[]}
        try:
            with opener.open(urllib.request.Request(url,method='HEAD',headers={'Accept-Encoding':'identity','User-Agent':args.user_agent}),timeout=15) as r:
                result['head'] = {'status':r.status,'content_length':r.headers.get('Content-Length'),
                    'accept_ranges':r.headers.get('Accept-Ranges'),
                    'duration_or_bitrate_headers':[k for k in r.headers if re.search('duration|bitrate|bit-rate',k,re.I)]}
        except Exception as e:
            result['head']={'error':type(e).__name__}
            if isinstance(e, HTTPError):
                result['head']['status'] = e.code
        cache = []
        def read_at(offset,count):
            for start,data in cache:
                if start <= offset and offset+count <= start+len(data):
                    return data[offset-start:offset-start+count]
            if len(cache)>=4:
                raise ValueError('256 KiB probe budget exhausted')
            end = min(offset+65536,local['byte_size'])-1
            req=urllib.request.Request(url,headers={'Range':f'bytes={offset}-{end}','Accept-Encoding':'identity','User-Agent':args.user_agent})
            with opener.open(req,timeout=20) as r:
                if r.status != 206:
                    raise ValueError('Range not honored; body not downloaded')
                expected=f'bytes {offset}-{end}/{local["byte_size"]}'
                if r.headers.get('Content-Range') != expected:
                    raise ValueError('Content-Range mismatch')
                data=r.read(end-offset+2)
                if len(data)!=end-offset+1:
                    raise ValueError('Range body length mismatch')
            with (Path(args.videos)/(key+'.mp4')).open('rb') as fp:
                fp.seek(offset)
                equals=fp.read(len(data))==data
            if not equals:
                raise ValueError('Remote bytes differ from local')
            result['ranges'].append({'offset':offset,'bytes':len(data),'status':206,
                                      'content_range_total':local['byte_size'],'matches_local':equals})
            cache.append((offset,data))
            return data[:count]
        try:
            result['remote_metadata']=study.movie_header(read_at,local['byte_size'])
            result['duration_matches_local']=result['remote_metadata']['duration_seconds']==local['duration_seconds']
        except Exception as e:
            # Exceptions from local IO or HTTP libraries may contain private
            # paths or complete resource URLs. Publish only type/status.
            result['range_error'] = type(e).__name__
            if isinstance(e, HTTPError):
                result['range_error'] += f': HTTP Error {e.code}'
        result['body_bytes_read']=sum(r['bytes'] for r in result['ranges'])
        # The already cached local object is hashed; no full remote download.
        with (Path(args.videos)/(key+'.mp4')).open('rb') as fp:
            result['local_full_md5_matches_url_e']=hashlib.file_digest(fp,'md5').hexdigest()==key
        return result

    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
        results=list(pool.map(probe,chosen))
    summary={'resource_host':'play.udon.dance','transport':'HTTPS; OS resolver; no proxy','user_agent':args.user_agent,
             'sample_method':f'{len(decile_samples)} size-decile probes plus {len(tail_samples)} additional tail-moov probes, all from saved URLs',
             'probes':results,'successes':sum(r.get('duration_matches_local',False) for r in results),
             'body_bytes_read':sum(r['body_bytes_read'] for r in results)}
    (out/'http-probes.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding='utf-8')
    print(json.dumps({'catalogs':{k:{f:v for f,v in c.items() if f!='field_counts'} for k,c in catalogue.items()},'probes':summary},ensure_ascii=False,indent=2))


if __name__=='__main__':
    p=argparse.ArgumentParser()
    p.add_argument('--private',required=True)
    p.add_argument('--output',required=True)
    p.add_argument('--videos',required=True)
    p.add_argument('--user-agent',default='Mozilla/5.0')
    p.add_argument('--catalog-only',action='store_true')
    run(p.parse_args())
