"""Render the public aggregate evidence; requires an existing Matplotlib install."""
import argparse
import json
from pathlib import Path


def plot(directory, preview=None):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    from matplotlib.patches import Patch
    matplotlib.rcParams.update({'font.family':'DejaVu Sans', 'font.size':10,
                                'svg.fonttype':'none', 'svg.hashsalt':'cf-triad-v1'})
    colors={'http':'#be630b','https':'#176997'}
    pools=('A','B_uniform','B_stratified')
    labels=('v1427 A','Uniform B','Stratified B')
    fig, axes=plt.subplots(2,2,figsize=(11,7.6),layout='constrained')
    for index,n in enumerate((1,2)):
        data=json.loads((directory/f'window-{n:02}-summary.json').read_bytes())
        left,right=axes[index]
        left.set_title(f'W{n}: complete 2 MiB video deliveries')
        right.set_title('W1: same-window Speedtest top choice' if n==1 else 'W2: frozen W1 Speedtest top choice')
        for j,pool in enumerate(pools):
            p=data['pools'][pool]
            count=p['plannedCandidates']
            for method,offset in (('http',-.18),('https',.18)):
                x=j+offset
                m=p['methods'][method]
                good=m['fullSuccess']/count
                unknown=(m['screenedOut']+m['missing'])/count
                failed=1-good-unknown
                left.bar(x,good,.32,color=colors[method])
                left.bar(x,failed,.32,bottom=good,color='#dedede')
                left.bar(x,unknown,.32,bottom=good+failed,color='white',edgecolor='#777777',hatch='////',linewidth=.5)
                left.text(x,good+.015,f"{m['fullSuccess']}/{count}",ha='center',va='bottom',fontsize=8)
                if n==1:
                    ratio=p['prediction'][method]['selectedTop1RatioToActualBest']
                else:
                    hold=data['holdout'][pool][method]['speed']['topK']['1']
                    ratio=None if hold['unknown'] else hold['bestSelectedObservedRatio']
                if ratio is None:
                    right.text(x,.035,'N/A',ha='center',va='bottom',fontsize=8,rotation=90)
                else:
                    right.bar(x,ratio,.32,color=colors[method])
                    right.text(x,ratio+.015,('failed' if ratio==0 else f'{ratio:.0%}'),ha='center',va='bottom',fontsize=8,rotation=90 if ratio==0 else 0)
        for ax in (left,right):
            ax.set_xticks(range(3),labels)
            ax.set_ylim(0,1.13)
            ax.set_yticks([0,.25,.5,.75,1],['0%','25%','50%','75%','100%'])
            ax.grid(axis='y',alpha=.2)
            ax.set_axisbelow(True)
            ax.spines[['top','right']].set_visible(False)
        left.set_ylabel('Fraction of all planned candidates')
        right.set_ylabel('Delivered rate / observed pool best')
        right.axhline(1,color='#777777',linewidth=.8,linestyle='--')
    fig.suptitle('CF candidate pools and Speedtest prediction',fontsize=15)
    fig.legend(handles=[Patch(facecolor=colors['http'],label='HTTP complete'),
                        Patch(facecolor=colors['https'],label='HTTPS complete'),
                        Patch(facecolor='#dedede',label='Attempted, not complete'),
                        Patch(facecolor='white',edgecolor='#777777',hatch='////',label='Not measured after screening / missing')],
               loc='outside lower center',ncol=2,frameon=False)
    fig.savefig(directory/'comparison.svg',metadata={'Date':None})
    if preview:
        fig.savefig(preview,dpi=150)
    plt.close(fig)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('directory',type=Path)
    parser.add_argument('--preview',type=Path)
    args=parser.parse_args()
    plot(args.directory,args.preview)
