// Pure helpers of TagInput (#22): splitting typed or pasted text into
// tags and adding them to a list. Tested in taginput.spec.ts.

/** Characters that end a tag while typing or in pasted text. */
export const TAG_SEPARATOR = /[\s,]+/;

/** Whether a key ends the tag being typed (Space, Enter or a comma). */
export const isTagKey = (key: string) => key === ' ' || key === 'Enter' || key === ',';

/** The parts of typed or pasted text, split at commas and whitespace. */
export function splitTagText(text: string): string[] {
	return text.split(TAG_SEPARATOR).filter(Boolean);
}

/**
 * Adds raw tags to a list: each normalized, empty ones and duplicates
 * dropped, at most `max` in all. `overflow` holds the raw tags that did
 * not fit (they stay in the input).
 */
export function addTags(
	values: readonly string[],
	raws: readonly string[],
	normalize: (raw: string) => string = (s) => s.trim(),
	max = Infinity
): { values: string[]; overflow: string[] } {
	const out = [...values];
	const overflow: string[] = [];
	for (const raw of raws) {
		const tag = normalize(raw);
		if (!tag || out.includes(tag)) continue;
		if (out.length >= max) overflow.push(raw);
		else out.push(tag);
	}
	return { values: out, overflow };
}
