// Column sorting of the file list (#15) onto the API's sort values
// (docs/api/files.md "Listings"). The default "type" is name order with
// folders first; the Name header shows it as ascending.
import type { ListSort } from './api';

export type SortColumn = 'name' | 'size' | 'modified';

export function sortColumn(s: ListSort): SortColumn {
	const k = s.replace(/^-/, '');
	return k === 'size' || k === 'modified' ? k : 'name';
}

export function sortDirection(s: ListSort): 'ascending' | 'descending' {
	return s.startsWith('-') ? 'descending' : 'ascending';
}

/** The sort after activating a column's header. */
export function nextSort(current: ListSort, column: SortColumn): ListSort {
	if (column === 'name') return current === 'type' || current === 'name' ? '-name' : 'type';
	// Size and date start with the largest / newest.
	if (current === `-${column}`) return column;
	return `-${column}`;
}
