// Include-by-default selections of policies (#10, #20): a policy stores
// what it leaves out; views show what it covers.

/** Adds (exclude) or removes keys from an exclusion list, keeping order. */
export function setExcluded(excluded: string[], keys: string[], exclude: boolean): string[] {
	if (exclude) return [...new Set([...excluded, ...keys])];
	const drop = new Set(keys);
	return excluded.filter((k) => !drop.has(k));
}

/** How many of the items are covered (not excluded, not locked out). */
export function coverageCount(
	items: { key: string; locked?: string }[],
	excluded: string[]
): { included: number; total: number } {
	const out = new Set(excluded);
	return {
		included: items.filter((i) => !i.locked && !out.has(i.key)).length,
		total: items.length
	};
}

/** "2 stacks and 1 volume", "nothing": a short count of exclusions. */
export function countText(parts: [number, string, string][], none = 'nothing'): string {
	const words = parts
		.filter(([n]) => n > 0)
		.map(([n, one, many]) => `${n} ${n === 1 ? one : many}`);
	if (!words.length) return none;
	if (words.length === 1) return words[0];
	return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`;
}
