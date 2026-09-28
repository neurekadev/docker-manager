// Checks of the public forms (#16, #22 polish): submit buttons stay
// enabled (a browser's autofill may not report values before the user
// interacts), the controls keep `required`, and the forms check on submit
// and say what is missing next to the field. Pure.

/** A field's current value and the message when it is empty. */
export type RequiredField = readonly [value: string | null | undefined, message: string];

/** The messages of the required fields that are empty (blank counts as empty). */
export function requiredErrors<K extends string>(
	fields: Record<K, RequiredField>
): Partial<Record<K, string>> {
	const out: Partial<Record<K, string>> = {};
	for (const key of Object.keys(fields) as K[]) {
		const [value, message] = fields[key];
		if (!value?.trim()) out[key] = message;
	}
	return out;
}

/**
 * A submit-time message while its field is still empty: it disappears as
 * soon as the person fills the field, not only on the next submit.
 */
export function untilFilled(
	message: string | undefined,
	value: string | null | undefined
): string | undefined {
	return message && !value?.trim() ? message : undefined;
}

/** True when there are no messages. */
export function isValid(errors: Partial<Record<string, string>>): boolean {
	return Object.values(errors).every((m) => !m);
}

/**
 * The submitted text value of a named control (FormData), falling back to
 * the bound value: autofilled fields are read even when the browser did
 * not report them as input yet.
 */
export function submitted(data: FormData | null, name: string, bound: string): string {
	const v = data?.get(name);
	return typeof v === 'string' && v !== '' ? v : bound;
}
