// Image build progress in job views (#33, #264). Builds report BuildKit
// progress as job progress messages (internal/agent/buildrun), each
// optionally led by the image a Compose build section builds:
//
//   "silo-web: [builder 2/5] RUN make: started"   a step's state (started,
//                                                   done, cached, error)
//   "silo-web: [builder 2/5] RUN make\n<output>"  a chunk of its output
//   "silo-web: building", "building images"         other messages
//
// Views show the step in words ("Step 2/5: RUN make") with a progress bar
// that follows the steps, never the raw output, which the Build Output
// dialog shows in a terminal (OutputFormatter).

export type StepStatus = 'started' | 'done' | 'cached' | 'error';

export interface BuildStep {
	/** The image being built (Compose builds), when the message names one. */
	image?: string;
	/** The Dockerfile stage of a multi-stage build ("builder"). */
	stage?: string;
	index: number;
	total: number;
	/** The instruction, e.g. "RUN make". */
	instruction: string;
	/** The step's state; absent for a chunk of its output. */
	status?: StepStatus;
	/** The message carries the step's output. */
	output: boolean;
}

const MARKER = /\[(?:(\S+) )?(\d+)\/(\d+)\] /;
const STATUS = /: (started|done|cached|error)(: [\s\S]*)?$/;

/** The first line of a message (output chunks span many). */
export function firstLine(message: string): string {
	const i = message.indexOf('\n');
	return i < 0 ? message : message.slice(0, i);
}

/** Reads a numbered BuildKit step ("[builder 2/5] RUN make") from a progress message. */
export function parseBuildStep(message: string): BuildStep | null {
	const head = firstLine(message);
	const m = MARKER.exec(head);
	if (!m) return null;
	const index = Number(m[2]);
	const total = Number(m[3]);
	if (!(index >= 1 && total >= index)) return null;
	const image = head.slice(0, m.index).replace(/:\s*$/, '') || undefined;
	let instruction = head.slice(m.index + m[0].length);
	const output = head.length < message.length;
	let status: StepStatus | undefined;
	const s = output ? null : STATUS.exec(instruction);
	if (s) {
		status = s[1] as StepStatus;
		instruction = instruction.slice(0, s.index);
	}
	return { image, stage: m[1], index, total, instruction: instruction.trim(), status, output };
}

export interface BuildProgress {
	/** The step reported last (null before the first numbered step). */
	step: BuildStep | null;
	/**
	 * The build is what the job reports now (its last message is BuildKit's),
	 * not a later step of the job (a deploy applying what it built).
	 */
	active: boolean;
	/** Percent of the current image's steps done (null unless active). */
	percent: number | null;
	/** A BuildKit step or output was reported: there is output to show. */
	hasOutput: boolean;
}

/**
 * Where a build stands, from its progress messages (oldest first). Reads
 * back from the newest message to the last numbered step, so it stays
 * cheap while a busy build reports.
 */
export function buildProgress(messages: readonly string[]): BuildProgress {
	let step: BuildStep | null = null;
	for (let i = messages.length - 1; i >= 0 && !step; i--) step = parseBuildStep(messages[i]);
	// A step is output to show; without one, a chunk of output is.
	const hasOutput = !!step || messages.some((m) => m.includes('\n'));
	const last = messages.at(-1) ?? '';
	const active =
		!!step && (last.includes('\n') || !!parseBuildStep(last) || STATUS.test(firstLine(last)));
	if (!step || !active) return { step, active, percent: null, hasOutput };
	const finished = step.status === 'done' || step.status === 'cached';
	const done = step.index - (finished ? 0 : 1);
	return { step, active, percent: Math.round((done / step.total) * 100), hasOutput };
}

/** "silo-web · Step 2/5: RUN make" (the image only when the message names one). */
export function stepText(step: BuildStep): string {
	const lead = step.image ? `${step.image} · ` : '';
	return `${lead}Step ${step.index}/${step.total}: ${step.instruction}`;
}

/**
 * The line a job view shows for a progress message: a build step in
 * words, else the message's first line (never a block of output).
 */
export function progressText(message: string | undefined): string {
	if (!message) return '';
	const step = parseBuildStep(message);
	return step ? stepText(step) : firstLine(message);
}

const ESC = '\x1b[';
const dim = (s: string) => `${ESC}2m${s}${ESC}0m`;
const colour = (code: number, s: string) => `${ESC}${code}m${s}${ESC}0m`;

/**
 * Turns progress messages into terminal text for the Build Output dialog,
 * one message at a time. It remembers the step whose output came last, so
 * a step's heading is written once for a run of its output.
 */
export class OutputFormatter {
	#step = '';

	format(message: string): string {
		const head = firstLine(message);
		if (head.length < message.length) {
			const body = message.slice(head.length + 1);
			const heading = head === this.#step ? '' : colour(36, `=> ${head}`) + '\n';
			this.#step = head;
			return heading + body + (body.endsWith('\n') ? '' : '\n');
		}
		this.#step = '';
		const s = STATUS.exec(head);
		if (!s) return dim(head) + '\n';
		const what = head.slice(0, s.index);
		switch (s[1]) {
			case 'started':
				// Its output follows under this heading.
				this.#step = what;
				return colour(36, `=> ${what}`) + '\n';
			case 'cached':
				return dim(`=> CACHED ${what}`) + '\n';
			case 'error':
				return colour(31, `=> ERROR ${what}${s[2] ?? ''}`) + '\n';
			default:
				return '';
		}
	}
}
