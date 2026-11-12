import type {ComponentProps,PropsWithChildren,ReactNode} from 'react';
import {Button,Table,TableBody,TableCell,TableCellLayout,TableHeader,TableHeaderCell,TableRow,Text,Tooltip,type SortDirection,type SlotRenderFunction,type TableColumnDefinition} from '@fluentui/react-components';
import {ChevronLeftRegular,ChevronRightRegular} from '@fluentui/react-icons';
import {TableRegion} from './ui';

export type TableSort={sort:string;direction:SortDirection};
type RecordTableProps=PropsWithChildren<{label:string;regionLabel:string;busy?:boolean;className?:string}>;
export type RecordColumn<TItem>=Pick<TableColumnDefinition<TItem>,'columnId'|'renderCell'>&{
  label:string;className?:string;sortable?:boolean;defaultDirection?:SortDirection;description?:ReactNode;
  headerProps?:Pick<ComponentProps<typeof TableHeaderCell>,'aria-label'>;
  cellProps?:(item:TItem)=>Omit<ComponentProps<typeof TableCell>,'children'|'className'>;
};
type RecordDataTableProps<TItem>=Omit<RecordTableProps,'children'>&{
  columns:RecordColumn<TItem>[];items?:TItem[];getRowId:(item:TItem)=>string|number;
  sort:TableSort;onSortChange:(sort:TableSort)=>void;sortDisabled?:boolean;
};

// Server queries own the rows; Fluent owns table semantics and header interaction.
export function RecordTable({children,label,regionLabel,busy,className}:RecordTableProps) {
  return <TableRegion label={regionLabel}><Table size="small" className={'data-table'+(className?' '+className:'')} style={{tableLayout:'auto'}} aria-label={label} aria-busy={busy}>{children}</Table></TableRegion>;
}

export function RecordDataTable<TItem>({columns,items=[],getRowId,sort,onSortChange,sortDisabled,...props}:RecordDataTableProps<TItem>) {
  return <RecordTable {...props}>
    <TableHeader><TableRow>{columns.map(column=>{
      const id=String(column.columnId),direction=sort.sort===id?sort.direction:undefined;
      return <TableHeaderCell key={column.columnId} {...column.headerProps} className={column.className} sortable={column.sortable} sortDirection={direction}
        button={{as:'div',...(column.description?{tabIndex:0}:{}),...(column.sortable?{'aria-label':'按'+column.label+'排序',disabled:sortDisabled,disabledFocusable:sortDisabled,onClick:()=>onSortChange({sort:id,direction:direction===undefined?column.defaultDirection??'ascending':direction==='ascending'?'descending':'ascending'})}:{}),children:column.description?headerTooltip(column.description):undefined}}>
        {column.label}
      </TableHeaderCell>;
    })}</TableRow></TableHeader>
    <TableBody>{items.map(item=><TableRow key={getRowId(item)}>{columns.map(column=><TableCell key={column.columnId} {...column.cellProps?.(item)} className={column.className}>{column.renderCell(item)}</TableCell>)}</TableRow>)}</TableBody>
  </RecordTable>;
}

export function TableText({children,primary,description}:{children:string;primary?:boolean;description?:string}) {
  return <Tooltip content={children} relationship="label"><TableCellLayout tabIndex={0} appearance={primary?'primary':undefined} description={description}>{children}</TableCellLayout></Tooltip>;
}

function headerTooltip(content:ReactNode):SlotRenderFunction<ComponentProps<'div'>> {
  return (Component,props)=><Tooltip content={<>{content}</>} relationship="description"><Component {...props}/></Tooltip>;
}

export function paginationSummary(page:number,total:number,pageSize:number,unit='条') {
  const count=`共 ${total.toLocaleString()} ${unit}`;
  return total?`第${page}/${Math.ceil(total/pageSize).toLocaleString()}页 · ${count}`:count;
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
