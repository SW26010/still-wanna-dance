"""Render tracked SVG figures from public aggregates; private inputs are unnecessary."""
import argparse,json
from pathlib import Path
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
parser=argparse.ArgumentParser()
parser.add_argument('--data',default='docs/research/cache-priority/study-data.json')
parser.add_argument('--output-dir',default='docs/research/cache-priority/figures')
parser.add_argument('--preview-dir')
args=parser.parse_args()
d=json.loads(Path(args.data).read_text(encoding='utf-8'));out=Path(args.output_dir);out.mkdir(parents=True,exist_ok=True)
plt.rcParams.update({'font.family':'DejaVu Sans','font.size':10,'axes.spines.top':False,'axes.spines.right':False,'axes.grid':True,'grid.alpha':.18,'svg.fonttype':'path','svg.hashsalt':'still-wanna-dance-cache-study-20261004','figure.facecolor':'white'})
styles={'power_event_14_1':('Hyperbolic, 14 days','#076a94','-'),'exp_event_60':('Exponential, 60 days','#309565','--'),
 'mix_14_120_0.25':('Two-scale exponential','#8b64a8',':'),'current_log_last_7':('Current log-count / last-age','#a15aac','-'),
 'frequency':('Cumulative count (LFU-like)','#7f8c98','-'),'active_days':('Distinct active days','#82962e',':'),
 'last_request':('Last request (LRU-like)','#bc4d55','-'),'window_event_7':('Count in last 7 days','#c27942','--'),
 'window_event_30':('Count in last 30 days','#b9972d','-'),'power_event_14_1_per_byte':('Hyperbolic score / bytes (heuristic)','#252d35','--')}
def save(fig,name):
    svg=out/(name+'.svg')
    fig.savefig(svg,bbox_inches='tight',metadata={'Date':None})
    # Matplotlib's multiline path attributes include trailing spaces.
    # Newlines preserve the SVG path separators while keeping Git diffs clean.
    svg.write_text('\n'.join(line.rstrip() for line in svg.read_text(encoding='utf-8').splitlines())+'\n',encoding='utf-8',newline='\n')
    if args.preview_dir:
        p=Path(args.preview_dir);p.mkdir(parents=True,exist_ok=True);fig.savefig(p/(name+'.png'),dpi=155,bbox_inches='tight')
    plt.close(fig)
fig,ax=plt.subplots(figsize=(11.8,6.4))
models=['power_event_14_1','exp_event_60','current_log_last_7','window_event_30','window_event_7','frequency','active_days','last_request']
handles=[];limit=d['axis_limits']['weekly_candidate_songs_max']
for m in models:
    label,c,ls=styles[m];line,=ax.plot(np.arange(limit+1),100*np.array(d['count_curves'][m][:limit+1]),color=c,ls=ls,lw=2,label=label)
    handles.append(line)
ax.set(xlabel='Top-N songs',ylabel='Mean daily distinct-song coverage (%)',xlim=(0,limit),ylim=(0,85))
ax.set_xticks([0,500,1000,1500,2000,2500,3000,limit])
ax.text(.97,.04,f'Maximum historical candidate set: {limit:,} songs\nAll-candidate coverage: {100*d["axis_limits"]["song_coverage_ceiling"]:.2f}%',ha='right',va='bottom',transform=ax.transAxes,color='#56656e',fontsize=9)
fig.legend(handles=handles,ncol=2,loc='lower center',frameon=False,bbox_to_anchor=(.5,.005),fontsize=9)
fig.suptitle('One-week forecasts: 11 non-overlapping holdout windows',fontsize=14)
fig.tight_layout(rect=(0,.17,1,.94));save(fig,'ranking-coverage')

fig,ax=plt.subplots(figsize=(11.8,6.2))
models=['power_event_14_1','exp_event_60','frequency','last_request','power_event_14_1_per_byte']
budgets=np.array(d['byte_budgets_gib']);handles=[]
limit=d['axis_limits']['weekly_candidate_storage_gib_max']
inside=budgets<=limit
for m in models:
    label,c,ls=styles[m];line,=ax.plot(budgets[inside],100*np.array(d['byte_curves'][m])[inside],color=c,ls=ls,lw=2,label=label)
    handles.append(line)
ax.axhline(100*d['data']['mean_seen_resource_coverage_ceiling'],color='#999',ls=':',lw=1)
ax.set(xlabel='Video cache capacity (GiB, measured file lengths)',ylabel='Mean daily distinct-song coverage (%)',xlim=(0,limit),ylim=(0,85))
ax.set_xticks([0,20,40,60,80,100,120,limit],labels=['0','20','40','60','80','100','120',f'{limit:.2f}'])
for b in [10,20,50,100]:
    v=d['byte_summary']['power_event_14_1'][str(b)]['coverage']*100
    ax.scatter(b,v,color='#076a94',s=23,zorder=4)
    ax.annotate(f'{b} GiB: {v:.1f}%',xy=(b,v),xytext=(7,-19 if b==100 else 9),textcoords='offset points',fontsize=9,color='#076a94')
ax.text(.97,.04,f'Maximum historical candidates: {limit:.2f} GiB / {d["axis_limits"]["weekly_candidate_songs_max"]:,} songs\nFull current resource set: {d["data"]["full_catalog_gib"]:.2f} GiB\nCurrent size snapshot; frozen one-week forecasts',ha='right',va='bottom',transform=ax.transAxes,color='#56656e',fontsize=9)
fig.legend(handles=handles,ncol=2,loc='lower center',frameon=False,bbox_to_anchor=(.5,.005),fontsize=9)
fig.suptitle('Cache capacity and predicted demand coverage',fontsize=14)
fig.tight_layout(rect=(0,.16,1,.94));save(fig,'storage-benefit')

fig,axes=plt.subplots(1,3,figsize=(14,4.6))
for ax,n in zip(axes,[300,500,1000]):
    for m in ['mix_14_120_0.25','power_event_14_1','exp_event_60']:
        label,c,ls=styles[m];ax.plot(np.arange(1,31),100*np.array(d['decay'][m][str(n)]['curve']),label=label,color=c,ls=ls,lw=1.7)
    ax.set(xlabel='Day after ranking is frozen',ylabel='Daily distinct-song coverage (%)',xlim=(1,30),ylim=(0,100),title=f'Top-{n:,}; 3 non-overlapping months')
axes[0].legend(fontsize=8)
fig.suptitle('Thirty-day persistence: noisy demand, similar candidate performance',fontsize=13)
fig.tight_layout(rect=(0,0,1,.92));save(fig,'frozen-ranking-decay')

fig,axes=plt.subplots(1,2,figsize=(12.8,4.8))
intervals=[(0,10),(10,20),(20,50),(50,100)]
means=[]
for lo,hi in intervals:
    low=0 if lo==0 else d['byte_summary']['power_event_14_1'][str(lo)]['coverage']
    high=d['byte_summary']['power_event_14_1'][str(hi)]['coverage'];means.append(100*(high-low)/(hi-lo))
axes[0].bar([f'{lo}-{hi}' for lo,hi in intervals],means,color='#076a94')
axes[0].set(xlabel='Additional capacity interval (GiB)',ylabel='Coverage gain (percentage points / GiB)',title='Diminishing marginal benefit')
bs=[10,20,50,100];unused=[d['byte_summary']['power_event_14_1'][str(b)]['unrequested_storage_fraction_7d']*100 for b in bs]
axes[1].bar([str(b) for b in bs],unused,color='#7f8c98')
axes[1].set(xlabel='Cache capacity (GiB)',ylabel='Cached bytes not requested in that week (%)',ylim=(0,100),title='Unrequested cached bytes over one week')
fig.suptitle('Storage trade-offs for the 14-day hyperbolic policy',fontsize=13)
fig.tight_layout(rect=(0,0,1,.92));save(fig,'marginal-benefit')
print('Rendered four self-contained SVG figures from published aggregates.')
