import type {TableSort} from './data-table';

export type LibraryQuery=TableSort&{view:'songs'|'resources';q:string;filter:string;offset:number};
export const isLibraryAnchor=(anchor:string)=>/^\/?library(?:\?|$)/.test(anchor);

export function readLibraryQuery(anchor:string):LibraryQuery {
  const params=new URLSearchParams(anchor.split('?')[1]||'');
  const song=params.get('song'),resource=params.get('resource');
  const view=params.get('view')==='resources'||resource?'resources':'songs';
  const offset=Number(params.get('offset')||0);
  return {
    view,q:params.get('q')??(song?'id:'+song:resource?'md5:'+resource:''),
    filter:params.get('filter')||'',sort:params.get('sort')||'score',
    direction:params.get('direction')==='ascending'?'ascending':'descending',
    offset:Number.isInteger(offset)&&offset>=0&&offset<=10000000?offset:0,
  };
}

export function libraryAnchor(query:LibraryQuery) {
  const params=new URLSearchParams({view:query.view});
  if(query.q)params.set('q',query.q);
  if(query.filter)params.set('filter',query.filter);
  if(query.sort!=='score')params.set('sort',query.sort);
  if(query.direction!=='descending')params.set('direction',query.direction);
  if(query.offset)params.set('offset',String(query.offset));
  return '/library?'+params;
}

export const songHref=(id:string)=>'#/library?view=songs&q='+encodeURIComponent('id:'+id);
export const resourceHref=(key:string)=>'#/library?view=resources&q='+encodeURIComponent('md5:'+key);
