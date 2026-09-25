// Text helpers of list inputs (labels, paths, exclusions: one per line).

/** Lines of a textarea (trimmed, blanks dropped) → list. */
export function linesToList(text: string): string[] {
	return text
		.split('\n')
		.map((l) => l.trim())
		.filter(Boolean);
}

/** List → textarea lines. */
export function listToLines(list: string[] | undefined): string {
	return (list ?? []).join('\n');
}
