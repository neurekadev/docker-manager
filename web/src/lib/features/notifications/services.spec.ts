import { describe, expect, it } from 'vitest';
import {
	channelStatus,
	errorText,
	ERROR_TEXT,
	kindsSummary,
	sendsSummary,
	EVENT_GROUPS,
	EVENT_KINDS,
	allEvents,
	eventKind,
	eventsDetail,
	eventsOf,
	kindState,
	noEvents,
	pickKind,
	pickOutcome,
	picksOf,
	testOutcome,
	type EventSubscription
} from './model';
import {
	SERVICES,
	buildUrl,
	initialValues,
	parseUrl,
	serviceIdOf,
	serviceLabel,
	serviceSpec,
	splitUrl,
	type ServiceId,
	type Values
} from './services';

const build = (id: ServiceId, v: Values) => buildUrl(id, { ...initialValues(id), ...v });

describe('notification services (#142): friendly fields to Shoutrrr URLs', () => {
	it('converts pasted Discord, Slack and Teams webhook URLs', () => {
		expect(
			build('discord', {
				webhookUrl: 'https://discord.com/api/webhooks/693853386302554172/W3dE2OZz_4z-uHf'
			})
		).toBe('discord://W3dE2OZz_4z-uHf@693853386302554172');
		expect(
			build('discord', {
				webhookUrl: 'https://discordapp.com/api/webhooks/1/tok?thread_id=99'
			})
		).toBe('discord://tok@1?thread_id=99');
		// Assembled from parts so secret scanners don't take the fixture for a real webhook.
		const slackHook = [
			'https://hooks.slack.com/services',
			'T00000000',
			'B00000000',
			'X'.repeat(24)
		];
		expect(build('slack', { webhookUrl: slackHook.join('/') })).toBe(
			'slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook'
		);
		expect(
			build('teams', {
				workflowUrl:
					'https://prod-00.westus.logic.azure.com:443/workflows/abc/invoke?sig=x&sp=/a'
			})
		).toBe(
			'teams://?host=https%3A%2F%2Fprod-00.westus.logic.azure.com%3A443%2Fworkflows%2Fabc%2Finvoke%3Fsig%3Dx%26sp%3D%2Fa'
		);
		// Anything else is not a webhook URL of that service.
		expect(build('discord', { webhookUrl: 'https://example.com/api/webhooks/1/x' })).toBeNull();
		expect(build('slack', { webhookUrl: 'https://hooks.slack.com/workflows/x' })).toBeNull();
	});

	it('builds email (SMTP) URLs with escaped logins and each encryption', () => {
		const base = {
			host: 'smtp.example.com',
			port: '587',
			username: 'alerts@example.com',
			password: 'p@ss:w/rd#1+',
			from: 'docker-manager@example.com',
			to: 'ops@example.com, ops+prod@example.com'
		};
		expect(build('email', base)).toBe(
			'smtp://alerts%40example.com:p%40ss%3Aw%2Frd%231%2B@smtp.example.com:587/' +
				'?fromaddress=docker-manager@example.com&toaddresses=ops@example.com,ops%2Bprod@example.com&encryption=Auto'
		);
		expect(build('email', { ...base, encryption: 'implicit', port: '465' })).toContain(
			':465/?fromaddress='
		);
		expect(build('email', { ...base, encryption: 'implicit' })).toMatch(
			/&encryption=ImplicitTLS$/
		);
		expect(build('email', { ...base, encryption: 'explicit' })).toMatch(
			/&encryption=ExplicitTLS&requirestarttls=yes$/
		);
		expect(build('email', { ...base, encryption: 'none' })).toMatch(
			/&encryption=None&usestarttls=no$/
		);
		// No login: no user info at all.
		expect(build('email', { ...base, username: '', password: '' })).toMatch(
			/^smtp:\/\/smtp\.example\.com:587\/\?/
		);
		// The address names only a sender name other than the default.
		expect(build('email', { ...base, fromName: ' Hyperion Alerts ' })).toContain(
			'?fromaddress=docker-manager@example.com&fromname=Hyperion%20Alerts&toaddresses='
		);
		for (const fromName of ['Docker Manager', ''])
			expect(build('email', { ...base, fromName })).not.toContain('fromname');
		expect(initialValues('email').fromName).toBe('Docker Manager');
	});

	it('builds the push, chat and webhook services', () => {
		expect(build('telegram', { token: '123456:ABC-def_1', chats: '@ops, -1001234' })).toBe(
			'telegram://123456:ABC-def_1@telegram?chats=@ops,-1001234'
		);
		expect(build('ntfy', { topic: 'dm-alerts-7f3a' })).toBe('ntfy://ntfy.sh/dm-alerts-7f3a');
		expect(
			build('ntfy', {
				server: 'ntfy.example.com:8080',
				topic: 'ops',
				username: 'dm',
				password: 's3cret',
				connection: 'http'
			})
		).toBe('ntfy://dm:s3cret@ntfy.example.com:8080/ops?scheme=http');
		expect(
			build('gotify', { server: 'push.example.com/gotify/', token: 'AzyoeNS.D4iJLVa' })
		).toBe('gotify://push.example.com/gotify/AzyoeNS.D4iJLVa');
		expect(
			build('pushover', { userKey: 'uKey1', token: 'aTok2', devices: 'phone, tablet' })
		).toBe('pushover://shoutrrr:aTok2@uKey1/?devices=phone,tablet');
		expect(
			build('matrix', {
				server: 'matrix.example.com',
				username: 'dm',
				password: 'pw',
				rooms: '#ops:example.com, !abc:example.com'
			})
		).toBe('matrix://dm:pw@matrix.example.com/?rooms=%23ops:example.com,!abc:example.com');
		expect(build('webhook', { url: 'https://hooks.example.com/in?key=1' })).toBe(
			'generic+https://hooks.example.com/in?key=1&template=json'
		);
		expect(build('webhook', { url: 'http://10.0.0.5:8080/hook', format: 'plain' })).toBe(
			'generic+http://10.0.0.5:8080/hook'
		);
		expect(build('other', { url: ' bark://devicekey@api.day.app ' })).toBe(
			'bark://devicekey@api.day.app'
		);
	});

	it('refuses incomplete or malformed fields with the reason in words', () => {
		expect(serviceSpec('email').check(initialValues('email'))).toMatchObject({
			host: 'Enter the SMTP server.',
			from: 'Enter the sender address.',
			to: 'Enter at least one recipient.'
		});
		const email = {
			...initialValues('email'),
			host: 'https://smtp.example.com',
			port: '99999',
			from: 'not-an-address',
			to: 'a@example.com, nope',
			password: 'x'
		};
		expect(Object.keys(serviceSpec('email').check(email)).sort()).toEqual([
			'from',
			'host',
			'port',
			'to',
			'username'
		]);
		expect(build('email', email)).toBeNull();
		expect(serviceSpec('telegram').check({ token: 'abc', chats: ' , ' })).toEqual({
			token: 'A bot token looks like 123456789:AA… (digits, a colon, then letters).',
			chats: 'Enter at least one chat.'
		});
		expect(serviceSpec('ntfy').check({ ...initialValues('ntfy'), topic: 'a/b' }).topic).toBe(
			'Use a topic without spaces or slashes.'
		);
		expect(serviceSpec('other').check({ url: 'not a url' }).url).toMatch(/Shoutrrr URL/);
	});

	it('reads stored URLs back into fields, and round-trips them exactly', () => {
		const urls: [ServiceId, string][] = [
			['discord', 'discord://tok@123?thread_id=9&username=Docker%20Manager'],
			[
				'slack',
				'slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook?color=good'
			],
			['teams', 'teams://?host=https%3A%2F%2Fprod.example.com%2Fworkflows%2Fa&title=Alert'],
			['telegram', 'telegram://123456:ABC@telegram?chats=@ops,-100&preview=No'],
			[
				'email',
				'smtp://u%40x.com:p%3Aw@mail.example.com:465/?fromaddress=a@x.com&toaddresses=b@x.com,c%2Bd@x.com&encryption=ImplicitTLS'
			],
			[
				'email',
				'smtp://mail.example.com:25/?fromaddress=a@x.com&toaddresses=b@x.com&encryption=None&usestarttls=no'
			],
			[
				'email',
				'smtp://mail.example.com:587/?fromaddress=a@x.com&fromname=Ops%20Team&toaddresses=b@x.com&encryption=Auto'
			],
			['ntfy', 'ntfy://u:p@ntfy.example.com/topic?scheme=http&priority=5'],
			['gotify', 'gotify://push.example.com/sub/AzyoeNS.D4iJLVa?disabletls=yes'],
			['pushover', 'pushover://shoutrrr:tok@user/?devices=a,b'],
			['matrix', 'matrix://:token@matrix.example.com/?rooms=%23ops:example.com'],
			['webhook', 'generic+https://hooks.example.com/in?key=1&template=json'],
			['webhook', 'generic+http://10.0.0.5/hook']
		];
		for (const [id, url] of urls) {
			const parsed = parseUrl(url);
			expect(parsed.service, url).toBe(id);
			expect(buildUrl(parsed.service, parsed.values), url).toBe(url);
		}
		expect(parseUrl(urls[0][1]).values.webhookUrl).toBe(
			'https://discord.com/api/webhooks/123/tok?thread_id=9'
		);
		expect(parseUrl(urls[4][1]).values).toMatchObject({
			host: 'mail.example.com',
			port: '465',
			encryption: 'implicit',
			username: 'u@x.com',
			password: 'p:w',
			to: 'b@x.com, c+d@x.com',
			fromName: 'Docker Manager'
		});
		expect(parseUrl(urls[6][1]).values.fromName).toBe('Ops Team');
	});

	it('edits unknown services and unreadable shapes as the raw URL', () => {
		for (const url of [
			'bark://devicekey@api.day.app',
			'smtp://mail.example.com/?fromaddress=a@x.com&toaddresses=b@x.com&encryption=ExplicitTLS&usestarttls=no',
			'generic+https://hooks.example.com/in?template=custom',
			'slack://xoxb:123456789012-1234567890123-4mt0t4l1YL3g1T5L4cK70k3N@C001CH4NN3L'
		]) {
			expect(parseUrl(url), url).toEqual({ service: 'other', values: { url } });
			expect(buildUrl('other', { url }), url).toBe(url);
		}
	});

	it('splits URLs the way Shoutrrr reads them', () => {
		expect(splitUrl('smtp://u%40x:p@ss@host:25/?a=1&b=x+y')).toEqual({
			scheme: 'smtp',
			user: 'u@x',
			password: 'p@ss',
			host: 'host:25',
			path: '/',
			query: [
				['a', '1'],
				['b', 'x y']
			]
		});
		expect(splitUrl('no scheme')).toBeNull();
	});

	it('names services in words and maps API service names to the picker', () => {
		expect(SERVICES.map((s) => s.label)).toEqual([
			'Discord',
			'Slack',
			'Microsoft Teams',
			'Telegram',
			'Email (SMTP)',
			'ntfy',
			'Gotify',
			'Pushover',
			'Matrix',
			'Webhook',
			'Other (Shoutrrr URL)'
		]);
		expect(serviceIdOf('smtp')).toBe('email');
		expect(serviceIdOf('generic')).toBe('webhook');
		expect(serviceIdOf('bark')).toBe('other');
		expect(serviceLabel('smtp')).toBe('Email');
		expect(serviceLabel('generic')).toBe('Webhook');
		expect(serviceLabel('teams')).toBe('Microsoft Teams');
		expect(serviceLabel('bark')).toBe('Bark');
	});
});

describe('notification channels in words (#142)', () => {
	it('summarizes what a channel sends', () => {
		const all = allEvents();
		const only = (...kinds: string[]) => all.filter((e) => kinds.includes(e.kind));
		expect(kindsSummary(all)).toBe('All events');
		expect(kindsSummary(only('backup'))).toBe('Backups and restores');
		expect(kindsSummary(only('prune', 'backup'))).toBe('Backups and restores and prune');
		expect(kindsSummary(only('raid', 'disk_health'))).toBe('Disk health and RAID');
		expect(kindsSummary(only('raid', 'disk_health', 'memory', 'job_failed'))).toBe(
			'4 kinds of events'
		);
		// Not every outcome of a chosen kind.
		expect(kindsSummary([{ kind: 'backup', outcomes: ['failure'] }])).toBe(
			'Backups and restores (some outcomes)'
		);
		expect(
			kindsSummary(
				all.map((e): EventSubscription =>
					e.kind === 'prune' ? { kind: 'prune', outcomes: ['failure'] } : e
				)
			)
		).toBe('Every kind of event (some outcomes)');
		expect(kindsSummary([])).toBe('Nothing');
		expect(kindsSummary([{ kind: 'raid', outcomes: [] }])).toBe('Nothing');
		const name = (id: string) => (id === 'e1' ? 'prod' : undefined);
		const sends = (allEnvironments: boolean, environmentIds: string[], events = only('raid')) =>
			sendsSummary({ events, allEnvironments, environmentIds }, name);
		expect(sends(true, [], all)).toBe('All events, every environment');
		expect(sends(false, ['e1'])).toBe('RAID, prod');
		expect(sends(false, ['e1', 'e2'])).toBe('RAID, 2 environments');
		// A filter whose environments are all gone never reads as every environment.
		expect(sends(false, [])).toBe('RAID, no environment');
	});

	it('lists every sent kind with its outcomes for the tooltip', () => {
		expect(
			eventsDetail([
				{ kind: 'job_failed', outcomes: ['resolved', 'failure'] },
				{ kind: 'environment_offline', outcomes: ['critical', 'resolved'] },
				{ kind: 'updates', outcomes: ['success'] }
			])
		).toBe(
			[
				'Environment offline: Offline, Back online',
				'Image updates: Applied',
				'Other failed jobs: Failure, Resolved'
			].join('\n')
		);
	});

	it('defines the kinds and outcomes of "What to send" in the manager’s order', () => {
		expect(EVENT_KINDS.map((k) => k.kind)).toEqual([
			'disk_health',
			'raid',
			'temperature',
			'disk_space',
			'memory',
			'environment_offline',
			'backup',
			'prune',
			'updates',
			'job_failed'
		]);
		expect(EVENT_GROUPS.map((g) => g.label)).toEqual(['Hosts', 'Jobs']);
		const labels = (kind: string) => eventKind(kind)?.outcomes.map((o) => o.label);
		expect(labels('temperature')).toEqual(['Warning', 'Critical', 'Resolved']);
		expect(labels('environment_offline')).toEqual(['Offline', 'Back online']);
		expect(labels('backup')).toEqual(['Failure', 'Warning', 'Success']);
		expect(labels('prune')).toEqual(['Failure', 'Success']);
		expect(labels('updates')).toEqual(['Available', 'Failure', 'Applied']);
		expect(labels('job_failed')).toEqual(['Failure', 'Warning', 'Resolved']);
		expect(EVENT_KINDS.filter((k) => k.group === 'hosts').map((k) => k.label)).toEqual([
			'Disk health',
			'RAID',
			'Temperature',
			'Disk space',
			'Memory',
			'Environment offline'
		]);
	});

	it('picks outcomes per kind and sends them in display order', () => {
		const backup = eventKind('backup')!;
		let picks = picksOf([
			{ kind: 'prune', outcomes: ['success'] },
			{ kind: 'backup', outcomes: ['success', 'failure'] }
		]);
		expect(kindState(backup, picks)).toBe('some');
		expect(eventsOf(picks)).toEqual([
			{ kind: 'backup', outcomes: ['failure', 'success'] },
			{ kind: 'prune', outcomes: ['success'] }
		]);
		picks = pickOutcome(picks, 'backup', 'warning', true);
		expect(kindState(backup, picks)).toBe('all');
		picks = pickKind(picks, 'backup', false);
		expect(kindState(backup, picks)).toBe('none');
		expect(eventsOf(picks)).toEqual([{ kind: 'prune', outcomes: ['success'] }]);
		picks = pickOutcome(picks, 'prune', 'success', false);
		expect(noEvents(picks)).toBe(true);
		expect(eventsOf(pickKind(picks, 'raid', true))).toEqual([
			{ kind: 'raid', outcomes: ['warning', 'critical', 'resolved'] }
		]);
		// Unknown kinds and outcomes are dropped.
		expect(eventsOf(picksOf([{ kind: 'nope' as never, outcomes: ['failure'] }]))).toEqual([]);
		expect(allEvents()).toHaveLength(EVENT_KINDS.length);
	});

	it('shows Off, Not tested, Working and Failing with the reason', () => {
		expect(channelStatus({ enabled: false, lastResult: 'ok' })).toEqual({
			status: 'stopped',
			label: 'Off'
		});
		expect(channelStatus({ enabled: true })).toEqual({
			status: 'unknown',
			label: 'Not tested'
		});
		expect(channelStatus({ enabled: true, lastResult: 'ok' }).label).toBe('Working');
		expect(channelStatus({ enabled: true, lastResult: 'auth' })).toEqual({
			status: 'failed',
			label: 'Failing',
			reason: ERROR_TEXT.auth
		});
	});

	it('explains every error class and the test outcome in words', () => {
		for (const c of [
			'dns',
			'connect',
			'tls',
			'timeout',
			'auth',
			'http_4xx',
			'http_5xx',
			'redirect',
			'rejected',
			'invalid_url'
		])
			expect(errorText(c)).toMatch(/\.$/);
		expect(errorText('something_new')).toMatch(/could not be sent/);
		expect(testOutcome('Ops', { ok: true })).toEqual({
			ok: true,
			title: 'Test message sent to Ops'
		});
		expect(testOutcome('Ops', { ok: false, errorClass: 'timeout' })).toEqual({
			ok: false,
			title: 'The test message to Ops failed',
			body: ERROR_TEXT.timeout
		});
	});
});
