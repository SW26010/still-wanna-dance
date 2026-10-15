"""Render the saved empirical measurements; never reads runtime storage."""
import argparse
import csv
import json
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import numpy as np


def run(args):
    folder=Path(args.input)
    rows=list(csv.DictReader((folder/'measurements.csv').open(encoding='utf-8')))
    summary=json.loads((folder/'summary.json').read_text(encoding='utf-8'))
    plt.rcParams.update({'font.family':args.font,'axes.unicode_minus':False,
        'font.size':11,'axes.spines.top':False,'axes.spines.right':False,
        'axes.edgecolor':'#ccd5df','axes.labelcolor':'#34465a','xtick.color':'#536378',
        'ytick.color':'#536378','svg.hashsalt':'video-metadata-20261015'})
    fig, axes=plt.subplots(1,3,figsize=(17,5.8))
    fig.patch.set_facecolor('#f8fafc')
    configs=[('duration_seconds','时长','秒',np.arange(0,961,20),'#27857b'),
             ('overall_mbps','平均总码率','Mbps',np.arange(0,5.001,0.1),'#4b72bb'),
             ('size_mib','文件大小','MiB',np.arange(0,185,5),'#9d679e')]
    bins_data={}
    for ax,(field,title,unit,edges,color) in zip(axes,configs):
        values=np.array([float(r[field]) for r in rows])
        stat=summary[field]
        ax.set_facecolor('white')
        ax.axvspan(stat['p05'],stat['p95'],color=color,alpha=.09,zorder=0)
        counts,_,_=ax.hist(values,bins=edges,color=color,edgecolor='white',linewidth=.5,zorder=2)
        assert int(counts.sum())==len(values)
        bins_data[field]={'edges':edges.tolist(),'counts':counts.astype(int).tolist()}
        ax.axvline(stat['median'],color='#dd7146',ls='--',lw=1.8,label='中位数',zorder=3)
        ax.set_xlim(edges[0],edges[-1])
        ax.set_title(title,loc='left',fontweight='bold',fontsize=16,pad=48,color='#172b45')
        ax.text(0,1.055,f"中位数 {stat['median']:.2f} {unit}\nP5–P95  {stat['p05']:.2f}–{stat['p95']:.2f} {unit}",
                transform=ax.transAxes,color='#536378',fontsize=10.5,linespacing=1.65)
        ax.set_xlabel(unit,labelpad=10)
        ax.set_ylabel('视频数')
        ax.grid(axis='y',color='#e9edf2',zorder=0)
        ax.ticklabel_format(axis='y',style='plain')
        if field=='duration_seconds':
            ax.set_xticks([0,200,400,600,800])
            # The full-range main plot retains every long video. An inset makes
            # sparse tails visible without excluding or clipping observations.
            inset=ax.inset_axes([.57,.40,.40,.40])
            inset.hist(values,bins=edges,color=color)
            inset.set_yscale('log')
            inset.set_xlim(0,960)
            inset.tick_params(labelsize=7)
            inset.set_title('全范围 · 对数纵轴',fontsize=8,color='#536378')
            inset.set_xticks([0,400,800])
        ax.text(.98,.96,f"最大 {stat['max']:.2f}",transform=ax.transAxes,ha='right',va='top',fontsize=9,color='#536378')
    fig.suptitle('本地视频的时长、码率与大小分布',x=.045,y=.98,ha='left',fontsize=22,fontweight='bold',color='#172b45')
    fig.text(.045,.90,f"2026-10-15  ·  {len(rows):,} 个唯一 MP4  ·  {summary['total_gib']:.2f} GiB  ·  每个文件计一次，全部有效观测均纳入",fontsize=11,color='#536378')
    fig.text(.045,.035,'橙色虚线：中位数    淡色区域：P5–P95    码率 = 文件字节数 × 8 ÷ 容器时长（包含音频及封装）\n1 Mbps = 1,000,000 bit/s；1 MiB = 1,048,576 byte。曲库缓存样本，不按播放热度加权。',fontsize=10,color='#536378',linespacing=1.7)
    fig.subplots_adjust(left=.05,right=.985,top=.70,bottom=.21,wspace=.27)
    fig.savefig(folder/'histograms.svg',facecolor=fig.get_facecolor(),metadata={'Date':None})
    # Matplotlib leaves spaces at line ends in SVG paths. Normalize only that
    # whitespace so generated figures pass Git's whitespace checks.
    svg = folder/'histograms.svg'
    svg.write_text('\n'.join(line.rstrip() for line in svg.read_text(encoding='utf-8').splitlines())+'\n',
                   encoding='utf-8', newline='\n')
    Path(args.png).parent.mkdir(parents=True,exist_ok=True)
    fig.savefig(args.png,dpi=160,facecolor=fig.get_facecolor())
    (folder/'histogram-bins.json').write_text(json.dumps(bins_data,indent=2),encoding='utf-8',newline='\n')
    # All fallback error estimates are descriptive, evaluated on this snapshot.
    duration=np.array([float(r['duration_seconds']) for r in rows])
    rate=np.array([float(r['overall_mbps']) for r in rows])
    size=np.array([float(r['size_mib']) for r in rows])
    fallback={}
    for name,v,default in [('duration_seconds',duration,210),('overall_mbps',rate,1.8),('size_mib',size,45)]:
        err=np.abs(v-default)
        fallback[name]={'default':default,'mean_absolute_error':float(err.mean()),
                        'p90_absolute_error':float(np.percentile(err,90)),
                        'fraction_at_or_below_default':float((v<=default).mean())}
    fallback['fraction_at_or_below_3_mbps']=float((rate<=3).mean())
    fallback['fraction_at_or_below_80_mib']=float((size<=80).mean())
    fallback['fraction_at_or_below_285_seconds']=float((duration<=285).mean())
    fallback['consistent_triple_size_mib']=210*1.8e6/8/2**20
    fallback['size_duration_correlation']=float(np.corrcoef(size,duration)[0,1])
    (folder/'fallback-analysis.json').write_text(json.dumps(fallback,indent=2),encoding='utf-8',newline='\n')
    print(json.dumps(fallback,indent=2))


if __name__=='__main__':
    p=argparse.ArgumentParser()
    p.add_argument('--input',required=True)
    p.add_argument('--png',required=True)
    p.add_argument('--font',default='Microsoft YaHei',help='Installed font with Chinese glyphs')
    run(p.parse_args())
