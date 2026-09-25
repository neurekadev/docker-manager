// Step-up (#16): sensitive changes (security policy, groups and grants,
// invitations, factor resets, Recovery Key rotation, API token creation,
// credential administration) answer 403 step_up_required when the session
// has no recent authentication. withStepUp() runs the call, asks the user
// to confirm their identity once (StepUpDialog, mounted by the signed-in
// layout) and retries it. The server decides what needs a step-up; the UI
// never guesses.
import { ApiRequestError } from '$lib/api/client';

/** The API refused the change until the user re-authenticates. */
export function isStepUpRequired(e: unknown): boolean {
	return e instanceof ApiRequestError && e.apiError?.code === 'step_up_required';
}

/** The user closed the identity check: the change was not made. */
export class StepUpCancelledError extends Error {
	constructor() {
		super('The change was not made: it needs you to confirm your identity first.');
		this.name = 'StepUpCancelledError';
	}
}

export class StepUpPrompt {
	/** The dialog is open. */
	open = $state(false);
	#waiting: ((ok: boolean) => void)[] = [];

	/** Opens the identity check; resolves true once re-authenticated. */
	request(): Promise<boolean> {
		this.open = true;
		return new Promise((resolve) => this.#waiting.push(resolve));
	}

	/** Called by the dialog: re-authenticated (true) or dismissed (false). */
	settle(ok: boolean) {
		this.open = false;
		const waiting = this.#waiting;
		this.#waiting = [];
		for (const w of waiting) w(ok);
	}
}

export const stepUp = new StepUpPrompt();

/**
 * Runs fn; when the manager asks for a step-up, prompts once and retries.
 * Throws StepUpCancelledError when the user dismisses the prompt.
 */
export async function withStepUp<T>(
	fn: () => Promise<T>,
	prompt: StepUpPrompt = stepUp
): Promise<T> {
	try {
		return await fn();
	} catch (e) {
		if (!isStepUpRequired(e)) throw e;
		if (!(await prompt.request())) throw new StepUpCancelledError();
		return await fn();
	}
}
