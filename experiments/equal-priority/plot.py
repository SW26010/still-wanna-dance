"""Render vector research figures from the aggregate public summary (Matplotlib)."""
import argparse
import json
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.ticker import PercentFormatter

parser = argparse.ArgumentParser()
parser.add_argument('summary')
parser.add_argument('output')
parser.add_argument('--preview', help='Optional PNG preview in an ignored local directory')
args = parser.parse_args()
data = json.loads(Path(args.summary).read_text(encoding='utf-8'))
plt.rcParams.update({'font.family': 'DejaVu Sans', 'svg.fonttype': 'none', 'svg.hashsalt': 'equal-requester-20261026',
                     'font.size': 10, 'axes.spines.top': False, 'axes.spines.right': False})
colors = {'1': '#1676bc', '3': '#bf658e', '7': '#009c94', '14': '#202a35', '30': '#d99a00', '60': '#d25435', '365': '#9b62ae'}
selected = str(data['selectedHalfLifeDays'])
fig, axes = plt.subplots(2, 3, figsize=(14, 8), gridspec_kw={'height_ratios': [1.6, 1]}, sharex=True)
n = data['curves']['n']
for col, horizon in enumerate(data['horizons']):
    curves = data['curves']['periods']['all'][str(horizon)]
    for half, color in colors.items():
        label = half + '-day half-life' + (' (selected)' if half == selected else '')
        width = 2.7 if half == selected else 1.6
        axes[0, col].plot(n, curves[half], color=color, linewidth=width, label=label)
        if half != selected:
            axes[1, col].plot(n, [(a-b)*100 for a, b in zip(curves[half], curves[selected])], color=color, linewidth=1.5)
    count = data['summary']['all'][str(horizon)][selected]['windows']
    axes[0, col].set_title(f'{horizon}-day windows | {count} samples', loc='left', fontweight='bold')
    axes[0, col].set_ylim(0, 0.70)
    axes[0, col].yaxis.set_major_formatter(PercentFormatter(1))
    axes[1, col].axhline(0, color='#202a35', linewidth=1)
    axes[1, col].set_xlabel('Number of highest-ranked historical songs')
    for ax in axes[:, col]:
        ax.set_xlim(0, data['curves']['maxCandidates'])
        ax.grid(alpha=0.18)
axes[0, 0].set_ylabel('Mean daily distinct-song coverage')
axes[1, 0].set_ylabel(f'Difference from {selected} days (pp)')
handles, labels = axes[0, 0].get_legend_handles_labels()
fig.legend(handles, labels, loc='upper center', bbox_to_anchor=(0.5, 0.94), ncol=4, frameon=False)
fig.suptitle('Equal requester budgets: windows spread across the future', x=0.055, ha='left', fontsize=16, fontweight='bold', y=0.995)
fig.text(0.055, 0.955, '39 training cutoffs within the first 320 days; each frozen ranking is tested at 5 evenly spaced future starts per window length.', fontsize=10, color='#495765')
fig.text(0.055, 0.026, 'All windows end by 26 Oct 2026. Daily distinct songs; days, windows, training cutoffs and window lengths are averaged in order.', fontsize=9, color='#495765')
fig.text(0.055, 0.006, 'AUC stops at each training history\'s full candidate count. Drawn plateaus beyond that count add no area. Overlapping windows are not independent.', fontsize=9, color='#495765')
fig.tight_layout(rect=(0.02, 0.05, 1, 0.86), h_pad=2)
out = Path(args.output)
out.parent.mkdir(parents=True, exist_ok=True)
fig.savefig(out, metadata={'Date': None}, facecolor='white')
if args.preview:
    Path(args.preview).parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(args.preview, dpi=150, facecolor='white')
print(out)
