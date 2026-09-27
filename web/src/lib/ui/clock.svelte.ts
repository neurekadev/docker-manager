// A shared ticking clock (#22): live durations such as uptimes read
// `clock.now` inside a component or effect and re-render once a second.
// One interval serves every reader, and it runs only while at least one
// effect reads the clock (createSubscriber stops it when the last one is
// destroyed). Outside effects and during SSR it is a plain Date.now().
import { createSubscriber } from 'svelte/reactivity';

/** Tick interval in milliseconds. */
export const TICK_MS = 1000;

class Clock {
	#subscribe = createSubscriber((update) => {
		// Tick on second boundaries so every reader changes together.
		let interval: ReturnType<typeof setInterval> | undefined;
		const timeout = setTimeout(
			() => {
				update();
				interval = setInterval(update, TICK_MS);
			},
			TICK_MS - (Date.now() % TICK_MS)
		);
		return () => {
			clearTimeout(timeout);
			if (interval !== undefined) clearInterval(interval);
		};
	});

	/** The current time in milliseconds; reactive (ticks every second). */
	get now(): number {
		this.#subscribe();
		return Date.now();
	}
}

export const clock = new Clock();
