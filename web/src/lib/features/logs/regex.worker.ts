// The log viewer's regular expression search (#8) off the page's thread:
// the page terminates this worker when a batch runs past its time limit,
// so a pattern that backtracks for minutes cannot freeze the tab.
import { regexMatches } from './format';
import type { RegexReply, RegexRequest } from './regex-search.svelte';

self.onmessage = (e: MessageEvent<RegexRequest>) => {
	const { id, pattern, texts } = e.data;
	const reply: RegexReply = { id, results: regexMatches(pattern, texts) };
	self.postMessage(reply);
};
