// Build progress in job views (#264): BuildKit steps read from the job's
// progress messages, shown in words with a bar that follows the steps, and
// the output written to the Build Output dialog's terminal.
import { describe, expect, it } from 'vitest';
import {
	buildProgress,
	OutputFormatter,
	parseBuildStep,
	progressText,
	stepText
} from './build-progress';

describe('parseBuildStep', () => {
	it('reads the image, stage, position, instruction and state', () => {
		expect(parseBuildStep('silo-web: [builder 2/5] RUN make: started')).toEqual({
			image: 'silo-web',
			stage: 'builder',
			index: 2,
			total: 5,
			instruction: 'RUN make',
			status: 'started',
			output: false
		});
		expect(parseBuildStep('[3/3] COPY app /app: cached')).toMatchObject({
			image: undefined,
			stage: undefined,
			index: 3,
			total: 3,
			instruction: 'COPY app /app',
			status: 'cached'
		});
		expect(
			parseBuildStep('ghcr.io/me/web:1.2: [1/2] FROM alpine: error: not found')
		).toMatchObject({
			image: 'ghcr.io/me/web:1.2',
			instruction: 'FROM alpine',
			status: 'error'
		});
	});

	it('reads a chunk of output by its first line only', () => {
		expect(parseBuildStep('web: [2/4] RUN echo done: done\nreally: done')).toMatchObject({
			instruction: 'RUN echo done: done',
			status: undefined,
			output: true
		});
	});

	it('ignores messages without a numbered step', () => {
		for (const m of [
			'building images',
			'silo-web: building',
			'silo-web: [internal] load build definition from Dockerfile: done',
			'exporting to image: started',
			'[0/0] nothing'
		])
			expect(parseBuildStep(m)).toBeNull();
	});
});

describe('buildProgress', () => {
	it('follows the last step while the build reports', () => {
		expect(buildProgress(['loaded silo: building web'])).toEqual({
			step: null,
			active: false,
			percent: null,
			hasOutput: false
		});
		const running = buildProgress([
			'web: [1/4] FROM alpine: done',
			'web: [2/4] RUN make: started',
			'web: [2/4] RUN make\nok'
		]);
		expect(running).toMatchObject({ active: true, percent: 25, hasOutput: true });
		expect(running.step && stepText(running.step)).toBe('web · Step 2/4: RUN make');
		expect(buildProgress(['web: [4/4] COPY . .: cached'])).toMatchObject({ percent: 100 });
	});

	it('lets go once the job moves on from the build', () => {
		const after = buildProgress(['web: [4/4] COPY . .: done', 'applying 1a2b3c']);
		expect(after).toMatchObject({ active: false, percent: null, hasOutput: true });
	});
});

describe('progressText', () => {
	it('never shows a block of output', () => {
		expect(progressText('web: [2/4] RUN make\nline 1\nline 2')).toBe(
			'web · Step 2/4: RUN make'
		);
		expect(progressText('pulling images\nmore')).toBe('pulling images');
		expect(progressText(undefined)).toBe('');
	});
});

describe('OutputFormatter', () => {
	it('writes each step once as a heading over its output', () => {
		const f = new OutputFormatter();
		const out = [
			'building images',
			'web: [2/4] RUN make: started',
			'web: [2/4] RUN make\nline 1',
			'web: [2/4] RUN make\nline 2\n',
			'web: [2/4] RUN make: done',
			'web: [3/4] COPY . .: cached',
			'web: [4/4] RUN test: error: exit code 1'
		].map((m) => f.format(m));
		// Without the colours (ESC [ <n> m).
		const plain = out
			.join('')
			.split('\x1b[')
			.map((part, i) => (i ? part.replace(/^\d+m/, '') : part))
			.join('');
		expect(plain).toBe(
			[
				'building images',
				'=> web: [2/4] RUN make',
				'line 1',
				'line 2',
				'=> CACHED web: [3/4] COPY . .',
				'=> ERROR web: [4/4] RUN test: exit code 1',
				''
			].join('\n')
		);
	});
});
