import { useCallback, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  flexRender, getCoreRowModel, getExpandedRowModel, getFilteredRowModel, getPaginationRowModel, getSortedRowModel, useReactTable,
  type ColumnDef, type ColumnFiltersState, type ColumnPinningState, type ExpandedState, type RowSelectionState, type SortingState, type VisibilityState,
} from '@tanstack/react-table';
import { useVirtualizer } from '@tanstack/react-virtual';
import { Button } from './button.js';
import { Checkbox } from './checkbox.js';
import { Input } from './input.js';
import { NativeSelect } from './native-select.js';
import { Popover, PopoverContent, PopoverTrigger } from './popover.js';

export interface DataColumn<T> {
  id: string; label: string; value: (row: T) => string | number | bigint | boolean | null | undefined;
  render?: (row: T) => ReactNode; width?: number; sortable?: boolean; filterable?: boolean;
}
export interface DataTableProps<T> {
  label: string; data: T[]; columns: readonly DataColumn<T>[]; getRowId: (row: T) => string;
  getChildren?: (row: T) => T[] | undefined; height?: number; pageSize?: number;
  selectable?: boolean; onSelectionChange?: (ids: string[]) => void; onActivate?: (row: T) => void;
  loading?: boolean; error?: string; partial?: boolean;
}

export function DataTable<T>({ label, data, columns: definitions, getRowId, getChildren, height = 400, pageSize, selectable, onSelectionChange, onActivate, loading, error, partial }: DataTableProps<T>) {
  const { t } = useTranslation('backplane.ui');
  const [sorting, setSorting] = useState<SortingState>([]), [filters, setFilters] = useState<ColumnFiltersState>([]);
  const [visibility, setVisibility] = useState<VisibilityState>({}), [order, setOrder] = useState<string[]>([]);
  const [pinning, setPinning] = useState<ColumnPinningState>({}), [selection, setSelection] = useState<RowSelectionState>({});
  const [expanded, setExpanded] = useState<ExpandedState>({}), [query, setQuery] = useState(''), [focused, setFocused] = useState<string>();
  const columns = useMemo<ColumnDef<T>[]>(() => definitions.map((definition) => ({
    id: definition.id, header: definition.label, accessorFn: definition.value,
    size: definition.width ?? 180, minSize: 80,
    enableSorting: definition.sortable !== false, enableColumnFilter: definition.filterable !== false,
    cell: ({ row }) => definition.render ? definition.render(row.original) : String(definition.value(row.original) ?? ''),
    filterFn: (row, id, value: string) => String(row.getValue(id) ?? '').toLowerCase().includes(value.toLowerCase()),
    sortingFn: (a, b, id) => {
      const left = a.getValue<string | number | bigint | boolean | null>(id), right = b.getValue<typeof left>(id);
      if (left === right) return 0; if (left == null) return -1; if (right == null) return 1;
      return left < right ? -1 : 1;
    },
  })), [definitions]);
  const table = useReactTable({
    data, columns, getRowId, getSubRows: getChildren,
    state: { sorting, columnFilters: filters, columnVisibility: visibility, columnOrder: order, columnPinning: pinning, rowSelection: selection, expanded, globalFilter: query },
    onSortingChange: setSorting, onColumnFiltersChange: setFilters, onColumnVisibilityChange: setVisibility,
    onColumnOrderChange: setOrder, onColumnPinningChange: setPinning, onExpandedChange: setExpanded, onGlobalFilterChange: setQuery,
    onRowSelectionChange: (updater) => {
      const next = typeof updater === 'function' ? updater(selection) : updater; setSelection(next);
      onSelectionChange?.(Object.keys(next).filter((key) => next[key]));
    },
    enableRowSelection: !!selectable, columnResizeMode: 'onChange',
    getColumnCanGlobalFilter: () => true,
    globalFilterFn: (row, id, value: string) => String(row.getValue(id) ?? '').toLowerCase().includes(value.toLowerCase()),
    getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel(), getFilteredRowModel: getFilteredRowModel(), getExpandedRowModel: getExpandedRowModel(),
    getPaginationRowModel: pageSize ? getPaginationRowModel() : undefined,
    initialState: { pagination: { pageIndex: 0, pageSize: pageSize ?? 100 } },
  });
  const rows = table.getRowModel().rows;
  const scroll = useRef<HTMLDivElement>(null);
  const key = useCallback((index: number) => rows[index].id, [rows]);
  const virtual = useVirtualizer({ count: rows.length, getScrollElement: () => scroll.current, estimateSize: () => 40, overscan: 6, getItemKey: key, scrollMargin: 80 });
  const rowWidth = table.getTotalSize() + (selectable ? 44 : 0) + (getChildren ? 44 : 0);
  const reorder = (id: string, delta: number) => {
    const current = table.getAllLeafColumns().map((column) => column.id); const at = current.indexOf(id); const next = at + delta;
    if (next >= 0 && next < current.length) { [current[at], current[next]] = [current[next], current[at]]; setOrder(current); }
  };
  const focusRow = (index: number) => {
    const row = rows[index]; if (!row) return; setFocused(row.id); virtual.scrollToIndex(index);
    requestAnimationFrame(() => scroll.current?.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(row.id)}"]`)?.focus());
  };
  const pinnedStyle = (column: ReturnType<typeof table.getAllLeafColumns>[number]) => {
    const pinned = column.getIsPinned();
    return { width: column.getSize(), minWidth: column.getSize(), position: pinned ? 'sticky' as const : 'relative' as const,
      left: pinned === 'left' ? column.getStart('left') : undefined, right: pinned === 'right' ? column.getAfter('right') : undefined,
      zIndex: pinned ? 2 : undefined, background: 'var(--background)' };
  };
  return <section aria-label={label} className="flex flex-col gap-3">
    <div className="flex flex-wrap items-center gap-2"><Input type="search" aria-label={t('filterTable', { name: label })} value={query} onChange={(event) => setQuery(event.target.value)} />
      <Popover><PopoverTrigger asChild><Button type="button" variant="outline">{t('columns')}</Button></PopoverTrigger>
        <PopoverContent className="w-96"><div className="flex flex-col gap-3">{table.getAllLeafColumns().map((column, index, all) => <div key={column.id} className="flex items-center gap-2">
          <Checkbox aria-label={t('showColumn', { name: String(column.columnDef.header) })} checked={column.getIsVisible()} disabled={column.getIsVisible() && table.getVisibleLeafColumns().length === 1} onCheckedChange={(value) => column.toggleVisibility(value === true)} />
          <span>{String(column.columnDef.header)}</span>
          <Button type="button" size="icon-sm" variant="ghost" aria-label={t('moveLeft', { name: String(column.columnDef.header) })} disabled={index === 0} onClick={() => reorder(column.id, -1)}>←</Button>
          <Button type="button" size="icon-sm" variant="ghost" aria-label={t('moveRight', { name: String(column.columnDef.header) })} disabled={index === all.length - 1} onClick={() => reorder(column.id, 1)}>→</Button>
          <NativeSelect aria-label={t('pinColumn', { name: String(column.columnDef.header) })} value={column.getIsPinned() || ''} onChange={(event) => column.pin(event.target.value === 'left' ? 'left' : event.target.value === 'right' ? 'right' : false)}>
            <option value="">{t('unpinned')}</option><option value="left">{t('pinLeft')}</option><option value="right">{t('pinRight')}</option>
          </NativeSelect>
        </div>)}</div></PopoverContent>
      </Popover>
    </div>
    {loading && <p role="status">{t('loading')}</p>}{error && <p role="alert">{error}</p>}{partial && <p role="status">{t('partialResults')}</p>}
    <div ref={scroll} style={{ height, overflow: 'auto' }} className="rounded-md border">
      <table aria-label={label} aria-rowcount={rows.length + 1} style={{ display: 'grid', width: rowWidth, minWidth: '100%' }}>
        <thead style={{ display: 'grid', position: 'sticky', top: 0, zIndex: 3, background: 'var(--background)' }}>
          {table.getHeaderGroups().map((group) => <tr key={group.id} style={{ display: 'flex', minHeight: 80 }}>
            {selectable && <th style={{ width: 44 }}><Checkbox aria-label={t('selectAllRows')} checked={table.getIsAllRowsSelected() || (table.getIsSomeRowsSelected() ? 'indeterminate' : false)} onCheckedChange={(value) => table.toggleAllRowsSelected(value === true)} /></th>}
            {getChildren && <th style={{ width: 44 }}><span className="sr-only">{t('expand')}</span></th>}
            {group.headers.map((header) => <th key={header.id} aria-sort={header.column.getIsSorted() === 'asc' ? 'ascending' : header.column.getIsSorted() === 'desc' ? 'descending' : 'none'} style={{ ...pinnedStyle(header.column), textAlign: 'left', padding: 8 }}>
              <button type="button" disabled={!header.column.getCanSort()} onClick={header.column.getToggleSortingHandler()} className="font-medium focus-visible:ring-2 focus-visible:ring-ring">{flexRender(header.column.columnDef.header, header.getContext())}{header.column.getIsSorted() === 'asc' ? ' ↑' : header.column.getIsSorted() === 'desc' ? ' ↓' : ''}</button>
              {header.column.getCanFilter() && <Input className="mt-1 h-7" aria-label={t('filterColumn', { name: String(header.column.columnDef.header) })} value={String(header.column.getFilterValue() ?? '')} onChange={(event) => header.column.setFilterValue(event.target.value)} />}
              <div role="separator" aria-orientation="vertical" aria-label={t('resizeColumn', { name: String(header.column.columnDef.header) })} aria-valuenow={header.getSize()} tabIndex={0}
                onMouseDown={header.getResizeHandler()} onTouchStart={header.getResizeHandler()} onKeyDown={(event) => { if (['ArrowLeft', 'ArrowRight'].includes(event.key)) { event.preventDefault(); header.column.resetSize(); table.setColumnSizing((old) => ({ ...old, [header.column.id]: Math.max(80, header.getSize() + (event.key === 'ArrowLeft' ? -10 : 10)) })); } }}
                style={{ position: 'absolute', top: 0, right: 0, width: 6, height: '100%', cursor: 'col-resize', touchAction: 'none' }} />
            </th>)}
          </tr>)}
        </thead>
        <tbody style={{ display: 'grid', height: virtual.getTotalSize(), position: 'relative' }}>
          {virtual.getVirtualItems().map((item) => {
            const row = rows[item.index];
            return <tr key={row.id} data-row-id={row.id} data-index={item.index} ref={virtual.measureElement} tabIndex={focused === row.id || !rows.some((row) => row.id === focused) && item.index === 0 ? 0 : -1} aria-selected={selectable ? row.getIsSelected() : undefined} aria-rowindex={item.index + 2}
              onClick={(event) => {
                if ((event.target as HTMLElement).closest('button, a, input, select, textarea, [role="checkbox"], [contenteditable="true"]')) return;
                event.currentTarget.focus(); onActivate?.(row.original);
              }} onFocus={() => setFocused(row.id)} onKeyDown={(event) => {
                if (event.target !== event.currentTarget) return;
                if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) { event.preventDefault(); focusRow(event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, item.index + (event.key === 'ArrowDown' ? 1 : -1)))); }
                if (event.key === ' ' && selectable) { event.preventDefault(); row.toggleSelected(); }
                if (event.key === 'Enter') onActivate?.(row.original);
              }} style={{ display: 'flex', position: 'absolute', transform: `translateY(${item.start - 80}px)`, width: '100%', minHeight: 40, cursor: onActivate ? 'pointer' : undefined, background: row.getIsSelected() ? 'var(--accent)' : undefined }} className="border-b focus-visible:outline-2 focus-visible:outline-ring">
              {selectable && <td style={{ width: 44, padding: 8 }}><Checkbox aria-label={t('selectRow', { name: row.id })} checked={row.getIsSelected()} onCheckedChange={(value) => row.toggleSelected(value === true)} /></td>}
              {getChildren && <td style={{ width: 44 }}>{row.getCanExpand() && <Button type="button" variant="ghost" size="icon-sm" aria-label={t(row.getIsExpanded() ? 'collapseRow' : 'expandRow', { name: row.id })} onClick={row.getToggleExpandedHandler()}>{row.getIsExpanded() ? '−' : '+'}</Button>}</td>}
              {row.getVisibleCells().map((cell) => <td key={cell.id} style={{ ...pinnedStyle(cell.column), ...(row.getIsSelected() ? { background: 'var(--accent)' } : {}), padding: 8, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}
            </tr>;
          })}
        </tbody>
      </table>
      {!loading && !rows.length && <p className="p-4">{t('noRows')}</p>}
    </div>
    <div className="flex items-center gap-2" role="status"><span>{t('rowCount', { count: table.getFilteredRowModel().rows.length })}</span>{selectable && <span>{t('selectedCount', { count: Object.values(selection).filter(Boolean).length })}</span>}</div>
    {pageSize && <nav aria-label={t('pagination')} className="flex gap-2"><Button type="button" variant="outline" disabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}>{t('previous')}</Button><span>{table.getState().pagination.pageIndex + 1} / {Math.max(1, table.getPageCount())}</span><Button type="button" variant="outline" disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>{t('next')}</Button></nav>}
  </section>;
}
