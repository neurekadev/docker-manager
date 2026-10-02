<script lang="ts" module>
	import type { ExecTarget } from './session.svelte';

	export interface TerminalChoice extends ExecTarget {
		/** Why it can't be used now (not running), or null. */
		unavailable?: string | null;
	}
</script>

<script lang="ts">
	// Container terminal (#8, #22): pick the container (a stack's services),
	// the shell (Detect Automatically: Bash if present, else sh; the agent
	// finds it in the container) and connect; xterm fills the space and
	// follows its size. `autoConnect` connects once on arrival (a link such
	// as "Open a terminal in silo-web"), never on a plain visit.
	// The status line shows the connection and the command started; an idle
	// warning appears at 25 minutes (the session closes at 30). A 422
	// command_not_found (or close code 4422 from older agents) says the
	// image has no such shell.
	import { onDestroy } from 'svelte';
	import Plug from '@lucide/svelte/icons/plug';
	import SquareTerminal from '@lucide/svelte/icons/square-terminal';
	import Unplug from '@lucide/svelte/icons/unplug';
	import { api, ApiRequestError, unwrap } from '$lib/api/client';
	import type { TerminalHandle } from '$lib/lazy';
	import { Badge, Button, Notice, Select, TerminalView } from '$lib/ui';
	import {
		canReconnect,
		ExecTerminal,
		SHELLS,
		shellName,
		type ExecSession,
		type Shell,
		type WebSocketLike
	} from './session.svelte';

	interface Props {
		choices: TerminalChoice[];
		/** Preselected container (key = containerId). */
		initial?: string | null;
		/** Accessible name, e.g. "Terminal of Silo". */
		label: string;
		/** Connect once as soon as the terminal is ready (arrived from a link). */
		autoConnect?: boolean;
	}

	let { choices, initial = null, label, autoConnect = false }: Props = $props();

	function createError(e: unknown, t: ExecTarget, s: Shell): Error {
		if (!(e instanceof ApiRequestError)) return e instanceof Error ? e : new Error(String(e));
		if (e.apiError?.code === 'command_not_found')
			return new Error(`${t.label} has no ${shellName(s)}. Choose another shell.`);
		switch (e.status) {
			case 403:
				return new Error(
					`You can't open terminals in ${t.label}. Ask the owner of this Docker Manager for “Open terminal”.`
				);
			case 404:
				return new Error(`${t.label} does not exist anymore.`);
			case 409:
				return new Error(`${t.label} isn't running. Start it to open a terminal.`);
			case 429:
				return new Error(
					'You have the most terminals open at once (4). Close one to open another.'
				);
			case 503:
				return new Error("The environment is offline. Connect again when it's back.");
			default:
				return new Error(e.message);
		}
	}

	const session = new ExecTerminal({
		create: async (t, body): Promise<ExecSession> => {
			try {
				return await unwrap(
					api.POST(
						'/api/v1/environments/{environmentId}/containers/{containerId}/exec-sessions',
						{
							params: {
								path: { environmentId: t.environmentId, containerId: t.containerId }
							},
							body
						}
					)
				);
			} catch (e) {
				throw createError(e, t, body.shell);
			}
		},
		remove: async (t, id) => {
			await unwrap(
				api.DELETE(
					'/api/v1/environments/{environmentId}/containers/{containerId}/exec-sessions/{sessionId}',
					{
						params: {
							path: {
								environmentId: t.environmentId,
								containerId: t.containerId,
								sessionId: id
							}
						}
					}
				)
			);
		},
		socket: (url, protocols) => new WebSocket(url, protocols) as unknown as WebSocketLike
	});

	let term = $state<TerminalHandle | null>(null);
	let selected = $state('');
	let shell = $state<Shell>('auto');
	const target = $derived(choices.find((c) => c.containerId === selected) ?? null);
	const busy = $derived(session.state === 'creating' || session.state === 'connecting');
	const open = $derived(session.state === 'open');

	$effect(() => {
		if (selected && choices.some((c) => c.containerId === selected)) return;
		const pick =
			choices.find((c) => c.containerId === initial) ??
			choices.find((c) => !c.unavailable) ??
			choices[0];
		selected = pick?.containerId ?? '';
	});

	session.onoutput = (bytes) => term?.write(bytes);

	function onready(t: TerminalHandle) {
		t.setInputEnabled(false);
		t.onData((d) => session.send(d));
		t.onResize(({ cols, rows }) => session.resize(cols, rows));
	}

	$effect(() => {
		term?.setInputEnabled(open);
		if (open) term?.focus();
	});

	// Arriving from "Open a terminal in …": connect once, when the terminal is
	// ready and the chosen container runs; a plain visit waits for Connect.
	let autoTried = false;
	$effect(() => {
		if (autoTried || !autoConnect || !term || !target) return;
		autoTried = true;
		if (!target.unavailable && session.state === 'idle') void connect();
	});

	async function connect() {
		if (!target || !term) return;
		term.clear();
		const size = term.fit();
		try {
			await session.connect(target, shell, size);
		} catch {
			// session.message says what happened
		}
	}

	onDestroy(() => {
		if (session.state === 'open' || session.state === 'connecting') void session.disconnect();
	});

	const statusTone = $derived(
		open ? 'ok' : busy ? 'info' : session.state === 'failed' ? 'danger' : 'neutral'
	);
	const statusText = $derived(
		open
			? `Connected to ${target?.label ?? ''}`
			: busy
				? 'Connecting…'
				: session.state === 'failed'
					? 'Disconnected'
					: session.state === 'closed'
						? 'Ended'
						: 'Not Connected'
	);
</script>

<section class="terminal" aria-label={label}>
	<form
		class="toolbar"
		onsubmit={(e) => {
			e.preventDefault();
			if (!open) void connect();
		}}
	>
		{#if choices.length > 1}
			<div class="pick">
				<Select
					label="Container"
					bind:value={selected}
					disabled={open || busy}
					options={choices.map((c) => ({
						value: c.containerId,
						label: c.unavailable ? `${c.label} (${c.unavailable})` : c.label
					}))}
				/>
			</div>
		{/if}
		<div class="cmd">
			<Select
				label="Shell"
				bind:value={() => shell, (v) => (shell = v as Shell)}
				disabled={open || busy}
				options={SHELLS.map((s) => ({ value: s.value, label: s.label }))}
			/>
		</div>
		<div class="buttons">
			{#if open}
				<Button variant="secondary" icon={Unplug} onclick={() => session.disconnect()}
					>Disconnect</Button
				>
			{:else}
				<Button
					type="submit"
					variant="primary"
					icon={Plug}
					loading={busy}
					disabled={!target || !!target.unavailable || !term}
					>{session.state === 'closed' || session.state === 'failed'
						? 'Connect Again'
						: 'Connect'}</Button
				>
			{/if}
		</div>
	</form>

	{#if target?.unavailable && !open}
		<div class="note">
			<Notice tone="info" live="none" title="{target.label} isn't running">
				Start it to open a terminal. Terminals run inside the container, never on the host.
			</Notice>
		</div>
	{/if}
	{#if session.idleWarning}
		<div class="note">
			<Notice
				tone="warn"
				live="alert"
				title="This terminal closes in {Math.ceil(session.idleSecondsLeft / 60)} minutes"
			>
				It has been idle for 25 minutes. Type anything to keep it open.
			</Notice>
		</div>
	{/if}
	{#if session.message && !open}
		<div class="note">
			<Notice
				tone={session.state === 'failed'
					? session.closeCode === 4422
						? 'warn'
						: 'danger'
					: 'info'}
				live="status"
				title={session.message}
			>
				{#if session.closeCode === 4422}Choose another shell, for example sh, and connect
					again.{/if}
				{#if session.closeCode !== null && !canReconnect(session.closeCode)}Close the other
					terminal first, or ask the owner of this Docker Manager about your access.{/if}
			</Notice>
		</div>
	{/if}

	<div class="screen">
		<TerminalView label="{label} Output" fit rawNewlines bind:terminal={term} {onready} />
		{#if session.state === 'idle'}
			<div class="overlay" aria-hidden="true">
				<SquareTerminal size={28} strokeWidth={1.5} />
				<p>
					Connect to open {shell === 'auto' ? 'a shell' : shellName(shell)} in {target?.label ??
						'the container'}.
				</p>
			</div>
		{/if}
	</div>

	<footer class="status">
		<Badge tone={statusTone} dot>{statusText}</Badge>
		{#if session.command && open}<span class="mono muted">{session.command}</span>{/if}
		<span class="muted">Closes after 30 minutes without activity.</span>
	</footer>
</section>

<style>
	.terminal {
		display: flex;
		flex-direction: column;
		min-height: 0;
		height: 100%;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
		overflow: hidden;
	}

	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
		padding: var(--space-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	.pick {
		width: 240px;
	}

	.cmd {
		width: 180px;
	}

	.buttons {
		display: flex;
		gap: var(--space-2);
	}

	.note {
		padding: var(--space-2) var(--space-3) 0;
	}

	.screen {
		position: relative;
		flex: 1;
		min-height: 200px;
		padding: var(--space-2);
	}

	.screen :global(.term) {
		border: 0;
	}

	.overlay {
		position: absolute;
		inset: var(--space-2);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-2);
		color: var(--text-muted);
		pointer-events: none;
	}

	.status {
		display: flex;
		align-items: center;
		gap: var(--space-4);
		min-height: 34px;
		padding: 0 var(--space-3);
		border-top: 1px solid var(--border-subtle);
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.pick,
		.cmd {
			width: 100%;
			max-width: none;
			flex-basis: 100%;
		}
	}
</style>
