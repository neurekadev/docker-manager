// Notification services (#142): the friendly fields of each service the
// channel dialog offers and how they become the Shoutrrr URL the manager
// stores (buildUrl), and back (parseUrl) when the owner shows a stored
// address. Services the dialog does not know, and addresses with a shape
// it cannot read, are edited as the raw URL ("Other"). Options a URL
// carries beyond the fields (a Discord username, ntfy tags, ...) are kept
// as they are. Pure (services.spec.ts); icons are in serviceIcons.ts.

export type ServiceId =
	| 'discord'
	| 'slack'
	| 'teams'
	| 'telegram'
	| 'email'
	| 'ntfy'
	| 'gotify'
	| 'pushover'
	| 'matrix'
	| 'webhook'
	| 'other';

export interface FieldOption {
	value: string;
	label: string;
}

export interface ServiceField {
	key: string;
	label: string;
	/** secret: a PasswordField (webhook URLs, tokens, passwords). */
	kind: 'text' | 'secret' | 'select';
	required?: boolean;
	placeholder?: string;
	description?: string;
	mono?: boolean;
	/** The value a new channel starts with. */
	initial?: string;
	options?: FieldOption[];
	inputmode?: 'numeric' | 'email';
	/** What to say while a required field is empty (default: "Enter the <label>."). */
	missing?: string;
}

export type Values = Record<string, string>;

export interface ServiceSpec {
	id: ServiceId;
	label: string;
	/** One line under the service picker: where to get what it asks for. */
	hint: string;
	fields: ServiceField[];
	/** The Shoutrrr URL, or null while a required field is missing or invalid. */
	build(v: Values): string | null;
	/** The fields of a stored URL, or null when this service can't read it. */
	parse(url: string): Values | null;
	/** Problems of the fields in words, by field key (empty: ready to save). */
	check(v: Values): Record<string, string>;
}

// --- URL helpers (not WHATWG URL: Shoutrrr URLs use custom schemes whose
// user info and query need exact, Go-compatible handling) ---

interface Parts {
	scheme: string;
	user: string | null;
	password: string | null;
	host: string;
	path: string;
	query: [string, string][];
}

const URL_RE = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)([^?#]*)(?:\?([^#]*))?(?:#.*)?$/;

function decode(s: string): string {
	try {
		return decodeURIComponent(s.replace(/\+/g, ' '));
	} catch {
		return s;
	}
}

function decodePart(s: string): string {
	try {
		return decodeURIComponent(s);
	} catch {
		return s;
	}
}

/** Splits a URL into its parts (null when it has no scheme://). */
export function splitUrl(url: string): Parts | null {
	const m = URL_RE.exec(url.trim());
	if (!m) return null;
	const [, scheme, authority, path, rawQuery] = m;
	const at = authority.lastIndexOf('@');
	let user: string | null = null;
	let password: string | null = null;
	let host = authority;
	if (at >= 0) {
		const info = authority.slice(0, at);
		host = authority.slice(at + 1);
		const colon = info.indexOf(':');
		user = decodePart(colon >= 0 ? info.slice(0, colon) : info);
		password = colon >= 0 ? decodePart(info.slice(colon + 1)) : null;
	}
	const query: [string, string][] = [];
	for (const pair of (rawQuery ?? '').split('&')) {
		if (!pair) continue;
		const eq = pair.indexOf('=');
		query.push(
			eq >= 0 ? [decode(pair.slice(0, eq)), decode(pair.slice(eq + 1))] : [decode(pair), '']
		);
	}
	return { scheme: scheme.toLowerCase(), user, password, host, path, query };
}

/** Encodes a user info part (everything but unreserved characters). */
function enc(s: string): string {
	return encodeURIComponent(s);
}

/** Encodes a query value, keeping the characters Shoutrrr lists read as-is. */
function encQuery(s: string): string {
	return encodeURIComponent(s)
		.replace(/%2C/gi, ',')
		.replace(/%40/g, '@')
		.replace(/%3A/gi, ':')
		.replace(/%2F/gi, '/');
}

function queryString(pairs: [string, string | undefined | null][], extra = ''): string {
	const out = pairs
		.filter(([, v]) => v !== undefined && v !== null && v !== '')
		.map(([k, v]) => `${k}=${encQuery(v as string)}`);
	if (extra) out.push(extra);
	return out.length ? `?${out.join('&')}` : '';
}

/**
 * Takes the known query keys out of a parsed query (case-insensitively, as
 * Shoutrrr reads them) and returns their values and the rest, re-encoded.
 */
function takeQuery(
	query: [string, string][],
	known: string[]
): { values: Record<string, string>; extra: string } {
	const values: Record<string, string> = {};
	const rest: string[] = [];
	for (const [k, v] of query) {
		const key = k.toLowerCase();
		if (known.includes(key)) values[key] = v;
		else rest.push(`${encodeURIComponent(k)}=${encQuery(v)}`);
	}
	return { values, extra: rest.join('&') };
}

/** A comma-separated list, trimmed, without empty entries. */
export function splitList(s: string): string[] {
	return s
		.split(',')
		.map((x) => x.trim())
		.filter(Boolean);
}

const HOST_RE = /^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?)(:\d{1,5})?$/;

/** A host name or address with an optional :port. */
export function validHost(s: string): boolean {
	return HOST_RE.test(s.trim());
}

/** A Title Case label inside a sentence: "Bot Token" → "bot token", acronyms kept ("API token"). */
function inSentence(label: string): string {
	return label
		.split(' ')
		.map((w) => (/^[A-Z][a-z]/.test(w) ? w[0].toLowerCase() + w.slice(1) : w))
		.join(' ');
}

function required(spec: ServiceField[], v: Values): Record<string, string> {
	const out: Record<string, string> = {};
	for (const f of spec) {
		if (f.required && !(v[f.key] ?? '').trim())
			out[f.key] = f.missing ?? `Enter the ${inSentence(f.label)}.`;
	}
	return out;
}

function withExtra(url: string, extra: string | undefined): string {
	if (!extra) return url;
	return url + (url.includes('?') ? '&' : '?') + extra;
}

const CONNECTION: ServiceField = {
	key: 'connection',
	label: 'Connection',
	kind: 'select',
	initial: 'https',
	options: [
		{ value: 'https', label: 'HTTPS' },
		{ value: 'http', label: 'HTTP (Unencrypted)' }
	]
};

// --- the services ---

const DISCORD_RE =
	/^https:\/\/(?:(?:ptb|canary)\.)?discord(?:app)?\.com\/api(?:\/v\d+)?\/webhooks\/(\d+)\/([A-Za-z0-9_-]+)\/?(?:\?(.*))?$/;

const discord: ServiceSpec = {
	id: 'discord',
	label: 'Discord',
	hint: 'In the Discord channel: Edit channel → Integrations → Webhooks → New webhook → Copy webhook URL.',
	fields: [
		{
			key: 'webhookUrl',
			label: 'Webhook URL',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: 'https://discord.com/api/webhooks/…'
		}
	],
	build(v) {
		const m = DISCORD_RE.exec((v.webhookUrl ?? '').trim());
		if (!m) return null;
		const [, id, token, query] = m;
		const q = splitUrl(`x://h?${query ?? ''}`)!.query;
		const { values, extra } = takeQuery(q, ['thread_id']);
		return withExtra(
			`discord://${enc(token)}@${id}${queryString([['thread_id', values.thread_id]])}`,
			[extra, v.extra].filter(Boolean).join('&')
		);
	},
	parse(url) {
		const p = splitUrl(url);
		if (
			!p ||
			p.scheme !== 'discord' ||
			!p.user ||
			!/^\d+$/.test(p.host) ||
			p.path.replace('/', '')
		)
			return null;
		const { values, extra } = takeQuery(p.query, ['thread_id']);
		const thread = values.thread_id ? `?thread_id=${encQuery(values.thread_id)}` : '';
		return {
			webhookUrl: `https://discord.com/api/webhooks/${p.host}/${p.user}${thread}`,
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.webhookUrl && !DISCORD_RE.test((v.webhookUrl ?? '').trim()))
			out.webhookUrl =
				'Paste the webhook URL Discord shows, starting with https://discord.com/api/webhooks/.';
		return out;
	}
};

const SLACK_RE =
	/^https:\/\/hooks\.slack\.com\/services\/([A-Za-z0-9]+)\/([A-Za-z0-9]+)\/([A-Za-z0-9]+)\/?$/;

const slack: ServiceSpec = {
	id: 'slack',
	label: 'Slack',
	hint: 'Create an incoming webhook for the channel in your Slack app (Incoming Webhooks) and copy its URL.',
	fields: [
		{
			key: 'webhookUrl',
			label: 'Webhook URL',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: 'https://hooks.slack.com/services/…'
		}
	],
	build(v) {
		const m = SLACK_RE.exec((v.webhookUrl ?? '').trim());
		if (!m) return null;
		return withExtra(`slack://hook:${m[1]}-${m[2]}-${m[3]}@webhook`, v.extra);
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'slack' || p.user?.toLowerCase() !== 'hook' || !p.password)
			return null;
		if (p.host.toLowerCase() !== 'webhook' || p.path.replace('/', '')) return null;
		const parts = p.password.split(/[-/,]/);
		if (parts.length !== 3 || parts.some((x) => !/^[A-Za-z0-9]+$/.test(x))) return null;
		return {
			webhookUrl: `https://hooks.slack.com/services/${parts.join('/')}`,
			extra: takeQuery(p.query, []).extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.webhookUrl && !SLACK_RE.test((v.webhookUrl ?? '').trim()))
			out.webhookUrl =
				'Paste the webhook URL Slack shows, starting with https://hooks.slack.com/services/.';
		return out;
	}
};

const teams: ServiceSpec = {
	id: 'teams',
	label: 'Microsoft Teams',
	hint: 'Create a Power Automate workflow with the trigger "When a Teams webhook request is received" and copy its URL.',
	fields: [
		{
			key: 'workflowUrl',
			label: 'Workflow URL',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: 'https://….logic.azure.com/workflows/…'
		}
	],
	build(v) {
		const u = (v.workflowUrl ?? '').trim();
		if (!/^https:\/\/\S+$/.test(u)) return null;
		return withExtra(`teams://?host=${encodeURIComponent(u)}`, v.extra);
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'teams' || p.host || p.user !== null || p.path.replace('/', ''))
			return null;
		const { values, extra } = takeQuery(p.query, ['host']);
		if (!values.host) return null;
		return { workflowUrl: values.host, extra };
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.workflowUrl && !/^https:\/\/\S+$/.test((v.workflowUrl ?? '').trim()))
			out.workflowUrl = 'Paste the workflow URL, starting with https://.';
		return out;
	}
};

const TELEGRAM_TOKEN_RE = /^\d+:[A-Za-z0-9_-]+$/;

const telegram: ServiceSpec = {
	id: 'telegram',
	label: 'Telegram',
	hint: 'Create a bot with @BotFather, add it to the chat, and use its token with the chat IDs or @channel names.',
	fields: [
		{
			key: 'token',
			label: 'Bot Token',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: '123456789:AA…'
		},
		{
			key: 'chats',
			label: 'Chats',
			kind: 'text',
			required: true,
			mono: true,
			placeholder: '@ops, -1001234567890',
			description: 'Chat IDs or @channel names, separated by commas.'
		}
	],
	build(v) {
		const token = (v.token ?? '').trim();
		const chats = splitList(v.chats ?? '');
		if (!TELEGRAM_TOKEN_RE.test(token) || !chats.length) return null;
		return withExtra(
			`telegram://${token}@telegram${queryString([['chats', chats.join(',')]])}`,
			v.extra
		);
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'telegram' || !p.user || p.password === null) return null;
		const { values, extra } = takeQuery(p.query, ['chats', 'channels']);
		const chats = values.chats ?? values.channels;
		if (!chats) return null;
		return { token: `${p.user}:${p.password}`, chats: splitList(chats).join(', '), extra };
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.token && !TELEGRAM_TOKEN_RE.test((v.token ?? '').trim()))
			out.token = 'A bot token looks like 123456789:AA… (digits, a colon, then letters).';
		if (!out.chats && !splitList(v.chats ?? '').length) out.chats = 'Enter at least one chat.';
		return out;
	}
};

const ENCRYPTION: Record<string, string> = {
	auto: 'encryption=Auto',
	none: 'encryption=None&usestarttls=no',
	explicit: 'encryption=ExplicitTLS&requirestarttls=yes',
	implicit: 'encryption=ImplicitTLS'
};

const MAIL_RE = /^[^\s@,]+@[^\s@,]+$/;

/** The sender name of an email without one of its own (notify.EmailFromName). */
export const EMAIL_FROM_NAME = 'Docker Manager';

const email: ServiceSpec = {
	id: 'email',
	label: 'Email (SMTP)',
	hint: 'Your mail server or provider: the SMTP host, port and, if it asks for one, a login.',
	fields: [
		{
			key: 'host',
			label: 'SMTP Server',
			kind: 'text',
			required: true,
			mono: true,
			placeholder: 'smtp.example.com'
		},
		{
			key: 'port',
			label: 'Port',
			kind: 'text',
			required: true,
			initial: '587',
			inputmode: 'numeric',
			description: '587 for STARTTLS, 465 for implicit TLS, 25 without encryption.'
		},
		{
			key: 'encryption',
			label: 'Encryption',
			kind: 'select',
			initial: 'auto',
			options: [
				{ value: 'auto', label: 'Auto' },
				{ value: 'explicit', label: 'Explicit TLS (STARTTLS)' },
				{ value: 'implicit', label: 'Implicit TLS' },
				{ value: 'none', label: 'None' }
			],
			description:
				'Auto uses implicit TLS on port 465 and STARTTLS elsewhere when the server offers it.'
		},
		{
			key: 'username',
			label: 'Username',
			kind: 'text',
			description: 'Optional. Leave empty for a server that needs no login.'
		},
		{ key: 'password', label: 'Password', kind: 'secret', description: 'Optional.' },
		{
			key: 'from',
			label: 'From',
			kind: 'text',
			required: true,
			missing: 'Enter the sender address.',
			inputmode: 'email',
			placeholder: 'docker-manager@example.com'
		},
		{
			key: 'fromName',
			label: 'From Name',
			kind: 'text',
			initial: EMAIL_FROM_NAME,
			placeholder: EMAIL_FROM_NAME,
			description: `The sender name mail programs show. Leave empty for ${EMAIL_FROM_NAME}.`
		},
		{
			key: 'to',
			label: 'To',
			kind: 'text',
			required: true,
			missing: 'Enter at least one recipient.',
			inputmode: 'email',
			placeholder: 'ops@example.com',
			description: 'Separate several addresses with commas.'
		}
	],
	build(v) {
		if (Object.keys(this.check(v)).length) return null;
		const user = (v.username ?? '').trim();
		const pass = v.password ?? '';
		const auth = user ? `${enc(user)}${pass ? `:${enc(pass)}` : ''}@` : '';
		// The default name is the manager's own (notify.EmailFromName): the
		// address names only another one.
		const fromName = (v.fromName ?? '').trim();
		const q = queryString(
			[
				['fromaddress', v.from.trim()],
				['fromname', fromName === EMAIL_FROM_NAME ? '' : fromName],
				['toaddresses', splitList(v.to).join(',')]
			],
			[ENCRYPTION[v.encryption || 'auto'], v.extra].filter(Boolean).join('&')
		);
		return `smtp://${auth}${v.host.trim()}:${v.port.trim()}/${q}`;
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'smtp' || p.path.replace('/', '')) return null;
		const hp = /^(.+?)(?::(\d+))?$/.exec(p.host);
		if (!hp) return null;
		const { values, extra } = takeQuery(p.query, [
			'fromaddress',
			'from',
			'fromname',
			'toaddresses',
			'to',
			'encryption',
			'usestarttls',
			'starttls',
			'requirestarttls'
		]);
		const mode = (values.encryption ?? 'auto').toLowerCase();
		const starttls = (values.usestarttls ?? values.starttls ?? 'yes').toLowerCase();
		const require = (values.requirestarttls ?? 'no').toLowerCase();
		const yes = (s: string) => ['yes', 'true', '1'].includes(s);
		let encryption: string;
		if (mode === 'implicittls' && !values.usestarttls && !values.requirestarttls)
			encryption = 'implicit';
		else if (mode === 'explicittls' && yes(starttls) && yes(require)) encryption = 'explicit';
		else if (mode === 'none' && !yes(starttls) && !yes(require)) encryption = 'none';
		else if (mode === 'auto' && yes(starttls) && !yes(require)) encryption = 'auto';
		else return null; // a combination the fields can't show: edit it as the URL
		return {
			host: hp[1],
			port: hp[2] ?? '25',
			encryption,
			username: p.user ?? '',
			password: p.password ?? '',
			from: values.fromaddress ?? values.from ?? '',
			fromName: values.fromname || EMAIL_FROM_NAME,
			to: splitList(values.toaddresses ?? values.to ?? '').join(', '),
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.host && !validHost(v.host))
			out.host = 'Enter a host name or address, without https://.';
		const port = Number((v.port ?? '').trim());
		if (!out.port && (!Number.isInteger(port) || port < 1 || port > 65535))
			out.port = 'Enter a port from 1 to 65535.';
		if (!out.from && !MAIL_RE.test(v.from.trim())) out.from = 'Enter one email address.';
		if (!out.to) {
			const bad = splitList(v.to).find((a) => !MAIL_RE.test(a));
			if (bad || !splitList(v.to).length)
				out.to = 'Enter email addresses separated by commas.';
		}
		if ((v.password ?? '') && !(v.username ?? '').trim())
			out.username = 'Enter the username that goes with the password.';
		return out;
	}
};

const ntfy: ServiceSpec = {
	id: 'ntfy',
	label: 'ntfy',
	hint: 'Subscribe to the topic in the ntfy app. On ntfy.sh anyone who knows the topic can read it, so pick a hard-to-guess one.',
	fields: [
		{
			key: 'server',
			label: 'Server',
			kind: 'text',
			required: true,
			mono: true,
			initial: 'ntfy.sh',
			description: 'ntfy.sh or your own server, with :port if it needs one.'
		},
		{ key: 'topic', label: 'Topic', kind: 'secret', required: true, mono: true },
		{
			key: 'username',
			label: 'Username',
			kind: 'text',
			description: 'Optional. For a server with access control.'
		},
		{
			key: 'password',
			label: 'Password or Access Token',
			kind: 'secret',
			description: 'Optional.'
		},
		CONNECTION
	],
	build(v) {
		if (Object.keys(this.check(v)).length) return null;
		const user = (v.username ?? '').trim();
		const auth =
			user || v.password ? `${enc(user)}${v.password ? `:${enc(v.password)}` : ''}@` : '';
		const q = queryString([['scheme', v.connection === 'http' ? 'http' : '']], v.extra);
		return `ntfy://${auth}${v.server.trim()}/${enc(v.topic.trim())}${q}`;
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'ntfy') return null;
		const topic = decodePart(p.path.replace(/^\//, ''));
		if (!topic || topic.includes('/')) return null;
		const { values, extra } = takeQuery(p.query, ['scheme']);
		return {
			server: p.host || 'ntfy.sh',
			topic,
			username: p.user ?? '',
			password: p.password ?? '',
			connection: values.scheme?.toLowerCase() === 'http' ? 'http' : 'https',
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.server && !validHost(v.server))
			out.server = 'Enter a host name, without https://.';
		if (!out.topic && /[/\s]/.test(v.topic.trim()))
			out.topic = 'Use a topic without spaces or slashes.';
		return out;
	}
};

const gotify: ServiceSpec = {
	id: 'gotify',
	label: 'Gotify',
	hint: 'In Gotify: Apps → Create application, then copy its token.',
	fields: [
		{
			key: 'server',
			label: 'Server',
			kind: 'text',
			required: true,
			mono: true,
			placeholder: 'gotify.example.com',
			description:
				'Host with :port and path if Gotify runs under one (gotify.example.com/gotify).'
		},
		{ key: 'token', label: 'Application Token', kind: 'secret', required: true, mono: true },
		CONNECTION
	],
	build(v) {
		if (Object.keys(this.check(v)).length) return null;
		const server = v.server.trim().replace(/\/+$/, '');
		const q = queryString([['disabletls', v.connection === 'http' ? 'yes' : '']], v.extra);
		return `gotify://${server}/${enc(v.token.trim())}${q}`;
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'gotify' || !p.host) return null;
		const path = p.path.replace(/\/+$/, '');
		const cut = path.lastIndexOf('/');
		const token = decodePart(path.slice(cut + 1));
		if (!token) return null;
		const { values, extra } = takeQuery(p.query, ['disabletls']);
		return {
			server: p.host + path.slice(0, Math.max(cut, 0)),
			token,
			connection: ['yes', 'true', '1'].includes((values.disabletls ?? '').toLowerCase())
				? 'http'
				: 'https',
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		const host = (v.server ?? '').trim().split('/')[0];
		if (!out.server && !validHost(host)) out.server = 'Enter a host name, without https://.';
		return out;
	}
};

const pushover: ServiceSpec = {
	id: 'pushover',
	label: 'Pushover',
	hint: 'Your user key is on the Pushover dashboard; create an application there for the API token.',
	fields: [
		{ key: 'userKey', label: 'User Key', kind: 'secret', required: true, mono: true },
		{ key: 'token', label: 'API Token', kind: 'secret', required: true, mono: true },
		{
			key: 'devices',
			label: 'Devices',
			kind: 'text',
			description:
				'Optional. Device names separated by commas; empty sends to all your devices.'
		}
	],
	build(v) {
		if (Object.keys(this.check(v)).length) return null;
		const q = queryString([['devices', splitList(v.devices ?? '').join(',')]], v.extra);
		return `pushover://shoutrrr:${enc(v.token.trim())}@${enc(v.userKey.trim())}/${q}`;
	},
	parse(url) {
		const p = splitUrl(url);
		if (!p || p.scheme !== 'pushover' || !p.password || !p.host || p.path.replace('/', ''))
			return null;
		const { values, extra } = takeQuery(p.query, ['devices']);
		return {
			userKey: decodePart(p.host),
			token: p.password,
			devices: splitList(values.devices ?? '').join(', '),
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		for (const k of ['userKey', 'token'])
			if (!out[k] && !/^[A-Za-z0-9]+$/.test((v[k] ?? '').trim()))
				out[k] = 'Use letters and digits only, as Pushover shows it.';
		return out;
	}
};

const matrix: ServiceSpec = {
	id: 'matrix',
	label: 'Matrix',
	hint: 'A Matrix account for Docker Manager that joined the rooms; its password or an access token.',
	fields: [
		{
			key: 'server',
			label: 'Homeserver',
			kind: 'text',
			required: true,
			mono: true,
			placeholder: 'matrix.example.com'
		},
		{
			key: 'username',
			label: 'Username',
			kind: 'text',
			description: 'Optional with an access token.'
		},
		{ key: 'password', label: 'Password or Access Token', kind: 'secret', required: true },
		{
			key: 'rooms',
			label: 'Rooms',
			kind: 'text',
			mono: true,
			placeholder: '#ops:example.com',
			description:
				'Optional. Room aliases or IDs separated by commas; empty sends to every joined room.'
		},
		CONNECTION
	],
	build(v) {
		if (Object.keys(this.check(v)).length) return null;
		const q = queryString(
			[
				['rooms', splitList(v.rooms ?? '').join(',')],
				['disabletls', v.connection === 'http' ? 'yes' : '']
			],
			v.extra
		);
		return `matrix://${enc((v.username ?? '').trim())}:${enc(v.password)}@${v.server.trim()}/${q}`;
	},
	parse(url) {
		const p = splitUrl(url);
		if (
			!p ||
			p.scheme !== 'matrix' ||
			!p.host ||
			p.password === null ||
			p.path.replace('/', '')
		)
			return null;
		const { values, extra } = takeQuery(p.query, ['rooms', 'room', 'disabletls']);
		return {
			server: p.host,
			username: p.user ?? '',
			password: p.password,
			rooms: splitList(values.rooms ?? values.room ?? '').join(', '),
			connection: ['yes', 'true', '1'].includes((values.disabletls ?? '').toLowerCase())
				? 'http'
				: 'https',
			extra
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.server && !validHost(v.server))
			out.server = 'Enter a host name, without https://.';
		return out;
	}
};

const webhook: ServiceSpec = {
	id: 'webhook',
	label: 'Webhook',
	hint: 'Docker Manager sends a POST request to the URL: JSON with title and message, or the message as plain text.',
	fields: [
		{
			key: 'url',
			label: 'Webhook URL',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: 'https://hooks.example.com/…'
		},
		{
			key: 'format',
			label: 'Message Format',
			kind: 'select',
			initial: 'json',
			options: [
				{ value: 'json', label: 'JSON (Title and Message)' },
				{ value: 'plain', label: 'Plain Text' }
			]
		}
	],
	build(v) {
		const u = (v.url ?? '').trim();
		if (!/^https?:\/\/\S+$/i.test(u)) return null;
		return `generic+${u}${v.format === 'plain' ? '' : (u.includes('?') ? '&' : '?') + 'template=json'}`;
	},
	parse(url) {
		const m = /^generic\+(https?:\/\/\S+)$/i.exec(url.trim());
		if (!m) return null;
		const [base, rawQuery] = m[1].split(/\?(.*)/s, 2);
		const pairs = (rawQuery ?? '').split('&').filter(Boolean);
		const template = pairs.filter((p) => /^template=/i.test(p));
		if (
			template.length > 1 ||
			(template.length === 1 && template[0].toLowerCase() !== 'template=json')
		)
			return null;
		const rest = pairs.filter((p) => !/^template=/i.test(p));
		return {
			url: base + (rest.length ? `?${rest.join('&')}` : ''),
			format: template.length ? 'json' : 'plain'
		};
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.url && !/^https?:\/\/\S+$/i.test((v.url ?? '').trim()))
			out.url = 'Enter a URL starting with https:// (or http://).';
		return out;
	}
};

const other: ServiceSpec = {
	id: 'other',
	label: 'Other (Shoutrrr URL)',
	hint: 'Any service Shoutrrr supports, as its service URL (for example bark://, mattermost:// or pagerduty://).',
	fields: [
		{
			key: 'url',
			label: 'Shoutrrr URL',
			kind: 'secret',
			required: true,
			mono: true,
			placeholder: 'service://…'
		}
	],
	build(v) {
		const u = (v.url ?? '').trim();
		return splitUrl(u) || /^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(u) ? u : null;
	},
	parse(url) {
		return { url };
	},
	check(v) {
		const out = required(this.fields, v);
		if (!out.url && !/^[A-Za-z][A-Za-z0-9+.-]*:\/\/\S*$/.test((v.url ?? '').trim()))
			out.url = 'Enter a Shoutrrr URL such as service://…, without spaces.';
		return out;
	}
};

/** Every service in the picker's order ("Other" last). */
export const SERVICES: ServiceSpec[] = [
	discord,
	slack,
	teams,
	telegram,
	email,
	ntfy,
	gotify,
	pushover,
	matrix,
	webhook,
	other
];

export function serviceSpec(id: ServiceId): ServiceSpec {
	return SERVICES.find((s) => s.id === id) ?? other;
}

/** The dialog's service for a Shoutrrr service name (the API's `service`). */
export function serviceIdOf(shoutrrr: string): ServiceId {
	switch (shoutrrr.toLowerCase()) {
		case 'discord':
		case 'slack':
		case 'teams':
		case 'telegram':
		case 'ntfy':
		case 'gotify':
		case 'pushover':
		case 'matrix':
			return shoutrrr.toLowerCase() as ServiceId;
		case 'smtp':
			return 'email';
		case 'generic':
			return 'webhook';
	}
	return 'other';
}

/** The service's name in words (lists, toasts). */
export function serviceLabel(shoutrrr: string): string {
	const id = serviceIdOf(shoutrrr);
	if (id === 'email') return 'Email';
	if (id !== 'other') return serviceSpec(id).label;
	return shoutrrr ? shoutrrr[0].toUpperCase() + shoutrrr.slice(1) : 'Other';
}

/** The initial field values of a service. */
export function initialValues(id: ServiceId): Values {
	const out: Values = {};
	for (const f of serviceSpec(id).fields) out[f.key] = f.initial ?? '';
	return out;
}

/**
 * The service and fields of a stored URL: the service its scheme names
 * when that service can read it, else "Other" with the URL as it is.
 */
export function parseUrl(url: string): { service: ServiceId; values: Values } {
	const p = splitUrl(url);
	const id = p ? serviceIdOf(p.scheme.split('+')[0]) : 'other';
	const values = serviceSpec(id).parse(url);
	if (values) return { service: id, values: { ...initialValues(id), ...values } };
	return { service: 'other', values: { url } };
}

/** The Shoutrrr URL of a service's fields, or null while they are incomplete. */
export function buildUrl(id: ServiceId, values: Values): string | null {
	const spec = serviceSpec(id);
	if (Object.keys(spec.check(values)).length) return null;
	return spec.build(values);
}
