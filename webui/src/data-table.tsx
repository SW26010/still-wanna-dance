import type {ComponentProps,PropsWithChildren,ReactNode} from 'react';
import {Button,Table,TableHeaderCell,Text,Tooltip,type SortDirection,type SlotRenderFunction} from '@fluentui/react-components';
import {ChevronLeftRegular,ChevronRightRegular} from '@fluentui/react-icons';
import {TableRegion} from './ui';

export type TableSort={sort:string;direction:SortDirection};

// Server queries own the rows; Fluent owns table semantics and header interaction.
export function RecordTable({children,label,regionLabel,busy}:PropsWithChildren<{label:string;regionLabel:string;busy?:boolean}>) {
  return <TableRegion label={regionLabel}><Table size="small" className="data-table" style={{tableLayout:'auto'}} aria-label={label} aria-busy={busy}>{children}</Table></TableRegion>;
}

export function SortHeader({column,label,sort,onSortChange,defaultDirection='ascending',disabled,className,description}:{
  column:string;label:string;sort:TableSort;onSortChange:(sort:TableSort)=>void;defaultDirection?:SortDirection;disabled?:boolean;
  className?:string;description?:ReactNode;
}) {
  const direction=sort.sort===column?sort.direction:undefined;
  return <TableHeaderCell sortable sortDirection={direction} className={className}
    button={{as:'div','aria-label':'按'+label+'排序',disabled,disabledFocusable:disabled,children:description?headerTooltip(description):undefined,onClick:()=>onSortChange({sort:column,direction:direction===undefined?defaultDirection:direction==='ascending'?'descending':'ascending'})}}>
    {label}
  </TableHeaderCell>;
}

function headerTooltip(content:ReactNode):SlotRenderFunction<ComponentProps<'div'>> {
  return (Component,props)=><Tooltip content={<>{content}</>} relationship="description"><Component {...props}/></Tooltip>;
}

export function DescriptionHeader({label,children}:PropsWithChildren<{label:string}>) {
  return <TableHeaderCell button={{as:'div',tabIndex:0,children:headerTooltip(children)}}>{label}</TableHeaderCell>;
}

export function TablePagination({label,summary,previous,next,canPrevious,canNext,disabled}:{
  label:string;summary:ReactNode;previous:()=>void;next:()=>void;canPrevious:boolean;canNext:boolean;disabled?:boolean;
}) {
  return <nav className="toolbar table-pagination" aria-label={label+'分页'}>
    <Text role="status">{summary}</Text>
    <div className="inline">
      <Button icon={<ChevronLeftRegular/>} aria-label={'上一页'+label} disabled={disabled||!canPrevious} onClick={previous}>上一页</Button>
      <Button icon={<ChevronRightRegular/>} iconPosition="after" aria-label={'下一页'+label} disabled={disabled||!canNext} onClick={next}>下一页</Button>
    </div>
  </nav>;
}
