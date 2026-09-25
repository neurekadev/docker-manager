// The exec WebSocket's URL (#8): the session's same-origin stream path on
// ws: or wss: matching the page (the ticket travels in the subprotocol,
// never in the URL).

export function socketUrl(streamPath: string, pageHref: string): string {
	const url = new URL(streamPath, pageHref);
	url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
	return url.toString();
}
