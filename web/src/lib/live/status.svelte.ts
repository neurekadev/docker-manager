// liveStatus: the state of the live connection for the UI shell (#22, #23).
//
// The shell (connection indicator, stale-data hints) imports `liveStatus`
// and reads its reactive fields; only the live client writes them.
//
//   state        connecting | live | reconnecting | polling | unauthenticated
//                | stopped | idle
//   since        when the state was entered (ms since epoch)
//   lastEventAt  when the last event or heartbeat arrived (ms), or null
//   failures     failed connection attempts since the last success
//   environments connection state of each environment the caller sees
//                (from `agent` events), e.g. { 'e1': 'offline' }
//
// `stale` is true whenever cached data may be behind the manager (anything
// but `live`): views may say so, but they keep showing the last data.

export type LiveState =
	'idle' | 'connecting' | 'live' | 'reconnecting' | 'polling' | 'unauthenticated' | 'stopped';

export class LiveStatus {
	state = $state<LiveState>('idle');
	since = $state(0);
	lastEventAt = $state<number | null>(null);
	failures = $state(0);
	environments = $state<Record<string, 'online' | 'offline'>>({});

	readonly stale: boolean = $derived(this.state !== 'live');

	set(state: LiveState, now: number): void {
		if (this.state === state) return;
		this.state = state;
		this.since = now;
	}
}

export const liveStatus = new LiveStatus();
