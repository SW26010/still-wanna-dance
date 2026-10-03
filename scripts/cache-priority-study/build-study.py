"""Generate aggregate, publication-safe study data from local private events.
No requester IDs, names, raw events, titles, local paths, or video hashes are exported.
"""
import argparse,json,math,hashlib
from pathlib import Path
from datetime import datetime,timedelta,timezone
import numpy as np
parser=argparse.ArgumentParser()
parser.add_argument('--events',required=True)
parser.add_argument('--benchmark',required=True)
parser.add_argument('--common-curves',required=True)
parser.add_argument('--sizes',required=True)
parser.add_argument('--output',required=True)
args=parser.parse_args()
events=json.loads(Path(args.events).read_text(encoding='utf-8'))
benchmark=json.loads(Path(args.benchmark).read_text(encoding='utf-8'))
common=json.loads(Path(args.common_curves).read_text(encoding='utf-8'))
snapshot=json.loads(Path(args.sizes).read_text(encoding='utf-8'))
DAY=86400000;GiB=2**30;TZ=timezone(timedelta(hours=8))
times=np.array([e['t'] for e in events],dtype=np.int64)
songs,ids=np.unique([e['id'] for e in events],return_inverse=True);S=len(songs)
resources={r['resource']:i for i,r in enumerate(snapshot['resources'])}
bytes_=np.array([r['bytes'] for r in snapshot['resources']],dtype=np.int64)
song_resource={r['songId']:resources[r['resource']] for r in snapshot['songs']}
mapping=np.array([song_resource.get(int(s),-1) for s in songs],dtype=int)
R=len(bytes_)
models=['power_event_14_1','exp_event_60','mix_14_120_0.25','current_log_last_7','frequency','last_request','window_event_7','power_event_14_1_per_byte']
folds=benchmark['fold_sets']['weekly_test']
max_known_cost=sum(bytes_[np.unique(mapping[mapping>=0])])/GiB
fold_song_limit=max(benchmark['folds'][fi]['training_songs'] for fi in folds)
fold_resource_costs=[]
for fi in folds:
    cut=int(datetime.fromisoformat(benchmark['folds'][fi]['cut']).replace(tzinfo=TZ).timestamp()*1000)
    observed=np.unique(ids[:np.searchsorted(times,cut)])
    mapped=mapping[observed];mapped=mapped[mapped>=0]
    fold_resource_costs.append(int(bytes_[np.unique(mapped)].sum())/GiB)
budgets=np.unique(np.r_[np.arange(0,math.ceil(max_known_cost)+1,.25),fold_resource_costs,[5,10,20,30,50,100,150,200,300]])
byte_curve={m:[] for m in models};used_curve={m:[] for m in models};unused_curve={m:[] for m in models}
byte_weighted={m:[] for m in models};ceiling=[];catalog_ceiling=[];folds_public=[]
count_cost={m:{str(n):[] for n in [100,300,500,1000,3000]} for m in models if not m.endswith('_per_byte')}
bootstrap_rng=np.random.default_rng(20261004)
count_models=['power_event_14_1','exp_event_60','mix_14_120_0.25','current_log_last_7','window_event_30','window_event_7','frequency','active_days','last_request']
count_fold_curves={m:[] for m in count_models}
def historical_scores(hi,ht,cut):
    age=(cut-ht)/DAY;counts=np.bincount(hi,minlength=S)
    last=np.full(S,-np.inf);np.maximum.at(last,hi,ht)
    scores={'frequency':counts.astype(float),'last_request':last,'current_log_last_7':np.log1p(counts)/(1+(cut-last)/DAY/7),
        'power_event_14_1':np.bincount(hi,weights=1/(1+age/14),minlength=S),
        'exp_event_60':np.bincount(hi,weights=np.exp2(-age/60),minlength=S),
        'window_event_30':np.bincount(hi,weights=(age<=30).astype(float),minlength=S),
        'window_event_7':np.bincount(hi,weights=(age<=7).astype(float),minlength=S)}
    song_days=np.unique(np.column_stack((hi,(ht+8*3600000)//DAY)),axis=0)
    scores['active_days']=np.bincount(song_days[:,0],minlength=S).astype(float)
    short=np.bincount(hi,weights=np.exp2(-age/14),minlength=S)
    long=np.bincount(hi,weights=np.exp2(-age/120),minlength=S)
    scores['mix_14_120_0.25']=.25*short/short.sum()+.75*long/long.sum()
    return counts,last,scores
for fi in folds:
    info=benchmark['folds'][fi]
    cut=int(datetime.fromisoformat(info['cut']).replace(tzinfo=TZ).timestamp()*1000)
    end=np.searchsorted(times,cut);stop=np.searchsorted(times,cut+7*DAY)
    hi=ids[:end];ht=times[:end];age=(cut-ht)/DAY
    counts=np.bincount(hi,minlength=S);seen=counts>0
    last=np.full(S,-np.inf);np.maximum.at(last,hi,ht)
    candidate=np.flatnonzero(seen&(mapping>=0))
    days=((times[end:stop]-cut)//DAY).astype(int)
    targets=[np.unique(ids[end:stop][days==d]) for d in np.unique(days)]
    future_resources=np.unique(mapping[np.unique(ids[end:stop])]);future_resources=future_resources[future_resources>=0]
    _,_,scores=historical_scores(hi,ht,cut)
    observed=np.flatnonzero(seen)
    for model in count_models:
        order=observed[np.lexsort((songs[observed],-last[observed],-counts[observed],-scores[model][observed]))]
        ranks=np.full(S,fold_song_limit+1,dtype=int);ranks[order]=np.arange(1,len(order)+1)
        # Histogram ranks to extend the original 0..3000 curve to the last
        # usable historical candidate, including plateau regions per fold.
        curve=np.mean([np.cumsum(np.bincount(ranks[target],minlength=fold_song_limit+2))[:fold_song_limit+1]/len(target) for target in targets],axis=0)
        count_fold_curves[model].append(curve)
    known_resources=np.unique(mapping[candidate]);known_set=set(known_resources.tolist())
    ceiling.append(float(np.mean([sum(mapping[s] in known_set for s in target)/len(target) for target in targets])))
    catalog_ceiling.append(float(np.mean([np.mean(mapping[target]>=0) for target in targets])))
    folds_public.append({**info,'mapped_training_songs':len(candidate),'known_unique_resources':len(known_resources),'known_resource_gib':float(bytes_[known_resources].sum()/GiB)})
    for model in models:
        base='power_event_14_1' if model.endswith('_per_byte') else model
        score=scores[base].copy()
        if model.endswith('_per_byte'):
            score[candidate]/=bytes_[mapping[candidate]]/GiB
        order=candidate[np.lexsort((songs[candidate],-last[candidate],-counts[candidate],-score[candidate]))]
        if model in count_cost:
            for n in [100,300,500,1000,3000]:count_cost[model][str(n)].append(float(bytes_[np.unique(mapping[order[:n]])].sum()/GiB))
        # First occurrence of a resource corresponds to its maximum associated
        # song score. Charging the resource once makes the ranked-song prefix
        # and the resource prefix equivalent in cumulative storage cost.
        order_resources=[];visited=set()
        for song in order:
            resource=int(mapping[song])
            if resource not in visited:visited.add(resource);order_resources.append(resource)
        order_resources=np.array(order_resources,dtype=int)
        cumulative=np.r_[0,np.cumsum(bytes_[order_resources])]
        occupied_count=np.searchsorted(cumulative,budgets*GiB,side='right')-1
        ranks=np.full(R,len(order_resources)+1,dtype=int);ranks[order_resources]=np.arange(1,len(order_resources)+1)
        day_curves=[];weighted=[]
        for target in targets:
            res=mapping[target];target_rank=np.full(len(target),len(order_resources)+1,dtype=int)
            target_rank[res>=0]=ranks[res[res>=0]]
            day_curves.append((target_rank[:,None]<=occupied_count[None,:]).mean(axis=0))
            # A declared-size-weighted proxy, not measured transmitted traffic.
            valid=res>=0;weights=bytes_[res[valid]].astype(float)
            weighted.append(((target_rank[valid,None]<=occupied_count[None,:])*weights[:,None]).sum(axis=0)/weights.sum())
        curve=np.mean(day_curves,axis=0);byte_curve[model].append(curve)
        used_curve[model].append(cumulative[occupied_count]/GiB)
        resource_used=np.isin(order_resources,future_resources)
        used_in_week=np.r_[0,np.cumsum(bytes_[order_resources]*resource_used)]
        unused=np.divide(cumulative[occupied_count]-used_in_week[occupied_count],cumulative[occupied_count],out=np.zeros(len(budgets)),where=cumulative[occupied_count]>0)
        unused_curve[model].append(unused);byte_weighted[model].append(np.mean(weighted,axis=0))
        assert np.all(np.diff(curve)>=-1e-12) and curve[0]==0
        assert np.all(cumulative[occupied_count]<=budgets*GiB)
        assert len(order_resources)==len(set(order_resources))
        assert abs(curve[-1]-ceiling[-1])<1e-12
mean_byte={m:np.mean(rows,axis=0) for m,rows in byte_curve.items()}
summary={}
for m in models:
    summary[m]={str(b):{'coverage':float(mean_byte[m][np.flatnonzero(budgets==b)[0]]),
        'mean_occupied_gib':float(np.mean(used_curve[m],axis=0)[np.flatnonzero(budgets==b)[0]]),
        'unrequested_storage_fraction_7d':float(np.mean(unused_curve[m],axis=0)[np.flatnonzero(budgets==b)[0]]),
        'size_weighted_coverage_proxy':float(np.mean(byte_weighted[m],axis=0)[np.flatnonzero(budgets==b)[0]])} for b in [5,10,20,30,50,100,150]}
reference='power_event_14_1'
ci_boot_indices=bootstrap_rng.integers(len(folds),size=(10000,len(folds)))
paired={}
for m in models:
    paired[m]={}
    for b in [10,20,50,100]:
        j=np.flatnonzero(budgets==b)[0];difference=np.array(byte_curve[reference])[:,j]-np.array(byte_curve[m])[:,j]
        ci=np.quantile(difference[ci_boot_indices].mean(axis=1),[.025,.975])
        paired[m][str(b)]={'reference_gain_pp':float(difference.mean()*100),'ci95_pp':(ci*100).tolist(),'winning_weeks':int((difference>0).sum())}
count_curves={m:np.mean(rows,axis=0).tolist() for m,rows in count_fold_curves.items()}
for m in count_models:
    previous=common['curves'][m] if m in common['curves'] else benchmark['curves'][m]['unique'][:3001]
    assert np.allclose(count_curves[m][:3001],previous,rtol=0,atol=1e-12),m
assert max(count_curves[m][-1] for m in count_models)-min(count_curves[m][-1] for m in count_models)<1e-12
decay_models=['mix_14_120_0.25','power_event_14_1','exp_event_60']
decay_endpoints={m:{str(n):[] for n in [100,300,500,1000]} for m in decay_models}
for ci in benchmark['fold_sets']['monthly_test']:
    info=benchmark['folds'][ci];cut=int(datetime.fromisoformat(info['cut']).replace(tzinfo=TZ).timestamp()*1000)
    end=np.searchsorted(times,cut);stop=np.searchsorted(times,cut+30*DAY)
    hi=ids[:end];ht=times[:end];counts,last,scores=historical_scores(hi,ht,cut)
    candidates=np.flatnonzero(counts);days=((times[end:stop]-cut)//DAY).astype(int)
    targets={d:np.unique(ids[end:stop][days==d]) for d in np.unique(days)}
    for m in decay_models:
        order=candidates[np.lexsort((songs[candidates],-last[candidates],-counts[candidates],-scores[m][candidates]))]
        ranks=np.full(S,4001,dtype=int);ranks[order]=np.arange(1,len(order)+1)
        for n in [100,300,500,1000]:
            row=np.full(30,np.nan)
            for d,target in targets.items():row[d]=np.mean(ranks[target]<=n)
            decay_endpoints[m][str(n)].append({'cut':info['cut'],'first_week':float(np.nanmean(row[:7])),'last_week':float(np.nanmean(row[23:30])),'mean30':float(np.nanmean(row))})
for m,values in decay_endpoints.items():
    for n,rows in values.items():
        assert abs(np.mean([r['mean30'] for r in rows])-benchmark['summaries'][m]['test30_monthly']['R'+n])<1e-12
result={'schemaVersion':1,'data':{'events':len(events),'songs':S,'first':datetime.fromtimestamp(times[0]/1000,TZ).isoformat(),'last':datetime.fromtimestamp(times[-1]/1000,TZ).isoformat(),'event_sha256':hashlib.sha256(json.dumps(events,separators=(',',':')).encode()).hexdigest(),'size_snapshot_sha256':snapshot['sha256'],'mapped_history_songs':int((mapping>=0).sum()),'mapped_events':int((mapping[ids]>=0).sum()),'full_catalog_gib':snapshot['summary']['fullCatalogBytes']/GiB,'all_observed_current_resources_gib':float(max_known_cost),'mean_seen_resource_coverage_ceiling':float(np.mean(ceiling)),'mean_full_current_catalog_coverage_ceiling':float(np.mean(catalog_ceiling))},
 'method':{'timezone':'Asia/Shanghai','frozen_ranking':True,'denominator':'daily distinct songs, including unseen and unmapped songs','averaging':'mean of observed days within each week, then equal weight across 11 non-overlapping weeks','size_axis':'Longest prefix of ranked currently mapped resources fitting byte budget; full files only, shared resource charged once; current snapshot, not historical file versions','candidate_policy':'Only pre-cutoff observed songs; unknown-size song excluded from admission, never from outcome denominator; any song sharing an admitted resource is covered under the snapshot mapping','density_policy':'Max associated observed song score divided by resource bytes, followed by the same prefix admission; heuristic, not a proven knapsack optimum','excluded_last_partial_date':'2026-10-03','uncertainty':'Paired bootstrap on 11 non-overlapping weeks; neighboring weeks can remain correlated; no simultaneous confidence band'},
 'selection_audit':{'stage':'early_benchmark_selection',
    'scope':'Historical selection record under the original pre-July validation rule; recommended refers to that stage, not the final article recommendation.',**benchmark['selection_audit']},
 'article_recommendation':{'recommended':'power_event_14_1','close_alternative':'exp_event_60',
    'scope':'Final engineering preference in article section 8; does not change the runtime algorithm or revise the early validation record.',
    'basis':'Prefer the simple 14-day hyperbolic formula given competitive coverage and small gains from more complex formulas; not a claim of unique optimality or a new independent validation.'},
 'folds':folds_public,'count_metrics':{m:benchmark['summaries'][m]['test7'] for m in count_models},'count_curves':count_curves,'common_auc_comparisons':common['summary'],
 'decay':{m:{str(k):benchmark['all_decay'][m][str(k)]['monthly'] for k in [100,300,500,1000]} for m in decay_models},'mean30':{m:benchmark['summaries'][m]['test30_monthly'] for m in decay_models},
 'byte_budgets_gib':budgets.tolist(),'byte_curves':{m:a.tolist() for m,a in mean_byte.items()},'byte_per_fold_curves':{m:np.array(a).tolist() for m,a in byte_curve.items()},'byte_summary':summary,'byte_paired_comparisons':paired}
result['data']['resource_size_mib_quantiles']={str(p):float(np.percentile(bytes_/2**20,p)) for p in [0,5,25,50,75,95,100]}
result['data']['observed_unique_resources']=int(len(np.unique(mapping[mapping>=0])))
result['count_mean_storage_gib']={m:{n:float(np.mean(rows)) for n,rows in values.items()} for m,values in count_cost.items()}
result['decay_endpoints']=decay_endpoints
result['axis_limits']={'weekly_candidate_songs_max':int(fold_song_limit),'weekly_candidate_songs_min':min(f['training_songs'] for f in folds_public),
    'weekly_candidate_storage_gib_max':max(fold_resource_costs),'weekly_candidate_storage_gib_min':min(fold_resource_costs),
    'weekly_candidate_resources_max':max(f['known_unique_resources'] for f in folds_public),
    'song_coverage_ceiling':count_curves['power_event_14_1'][-1],
    'definition':'Maximum pre-cutoff candidate count/cost across the 11 evaluated weeks; further expansion cannot add any candidate in any fold. All-period observed songs are not all available before these cutoffs.'}
out=Path(args.output);out.parent.mkdir(parents=True,exist_ok=True)
out.write_text(json.dumps(result,ensure_ascii=False,indent=2,allow_nan=False)+'\n',encoding='utf-8')
print(json.dumps({'data':result['data'],'byte_summary':{m:summary[m] for m in ['power_event_14_1','exp_event_60','frequency','power_event_14_1_per_byte']}},ensure_ascii=False,indent=2))
print('PASS: monotonic coverage, exact byte admission, unique shared-resource charges, and full-prefix coverage ceiling.')
