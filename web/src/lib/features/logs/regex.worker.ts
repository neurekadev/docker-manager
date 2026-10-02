// The log viewer's regular expression search (#8) off the page's thread:
// the page terminates this worker when a request runs past its time limit,
// so a pattern that backtracks for minutes cannot freeze the tab. `ready`
// tells the page the worker has loaded (the limit starts then); a pattern
// that throws (a RangeError of the engine) fails only its own request.
import { regexMatches } from './format';
import type { RegexReply, RegexRequest } from './regex-search.svelte';

self.onmessage = (e: MessageEvent<RegexRequest>) => {
	const { id, pattern, texts } = e.data;
	let reply: RegexReply;
	try {
		reply = { id, results: regexMatches(pattern, texts) };
	} catch {
		reply = { id, failed: true };
	}
	self.postMessage(reply);
};

const ready: RegexReply = { ready: true };
self.postMessage(ready);
