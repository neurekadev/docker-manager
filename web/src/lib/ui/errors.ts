// Error presentation helpers (#22 copy rules: say what happened and what
// to do next; never apologize; switch on apiError.code, never on message).
import { ApiRequestError } from '$lib/api/client';

export interface ErrorView {
	/** Plain-language summary for the user. */
	message: string;
	/** Stable API error code (switch on it), when the manager answered. */
	code?: string;
	/** Request ID for support and the manager logs. */
	requestId?: string;
	/** Repeating the same request later may succeed. */
	retryable: boolean;
	/** No response arrived (offline, manager unreachable). */
	network: boolean;
	status: number | null;
	fields: { field: string; message: string }[];
}

/** Normalizes anything thrown by the API layer into an ErrorView. */
export function errorView(e: unknown): ErrorView {
	if (e instanceof ApiRequestError) {
		if (e.network) {
			return {
				message:
					'Docker Manager could not reach the manager. Check the connection; this retries on its own.',
				retryable: true,
				network: true,
				status: null,
				fields: []
			};
		}
		const a = e.apiError;
		return {
			message: a?.message
				? sentence(a.message)
				: `The manager answered with HTTP ${e.status}.`,
			code: a?.code,
			requestId: a?.requestId,
			retryable: a?.retryable ?? (e.status !== null && e.status >= 500),
			network: false,
			status: e.status,
			fields: a?.details ?? []
		};
	}
	return {
		message: e instanceof Error ? sentence(e.message) : 'Something went wrong in the browser.',
		retryable: false,
		network: false,
		status: null,
		fields: []
	};
}

/** Short message of an error (dialogs, toasts). */
export function errorMessage(e: unknown): string {
	return errorView(e).message;
}

/** The error of one form field (e.g. "body.password"), if the API named it. */
export function fieldError(e: unknown, field: string): string | undefined {
	return errorView(e).fields.find((f) => f.field === field)?.message;
}

function sentence(s: string): string {
	const t = s.trim();
	if (!t) return t;
	const cap = t[0].toUpperCase() + t.slice(1);
	return /[.!?]$/.test(cap) ? cap : `${cap}.`;
}
