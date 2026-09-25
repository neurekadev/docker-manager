// What the stack layout shares with its tabs (#22 stack detail): the
// stack, its environment and the job tray. Tabs read the same queries
// (Svelte Query deduplicates them); this only saves re-deriving them.
import { getContext, setContext } from 'svelte';
import type { Environment } from '$lib/api/client';
import type { Stack } from './queries';
import type { JobTray } from './tray.svelte';

export interface StackPage {
	readonly id: string;
	readonly stack: Stack | undefined;
	readonly environment: Environment | undefined;
	readonly tray: JobTray;
}

const KEY = Symbol('stack-page');

export function provideStackPage(page: StackPage): void {
	setContext(KEY, page);
}

export function useStackPage(): StackPage {
	const p = getContext<StackPage | undefined>(KEY);
	if (!p) throw new Error('useStackPage outside the stack layout');
	return p;
}
