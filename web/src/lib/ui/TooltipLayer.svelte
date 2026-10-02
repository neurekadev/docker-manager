<script lang="ts">
	// App-wide tooltips (#22): every `title` in the app shows as a themed
	// tooltip instead of the browser's native one. Mounted once in the root
	// layout. On hover (after a short delay) or keyboard focus of an element
	// with a title, the title moves to data-dy-title while the tooltip shows
	// (so the native one never appears) and comes back when it hides; the
	// element is described by the tooltip meanwhile (unless its accessible
	// name is the same text). Touch input shows nothing (no hover), except
	// on an info tip (`data-dy-info`: InfoTip, Disclosure's hint): a tap
	// toggles it, any other tap hides it, and its click never activates the
	// <summary> or <label> around it. Explicit tooltips on controls use
	// Tooltip.svelte.
	import { onMount, tick } from 'svelte';
	import { infoAnchor, placeTooltip, tooltipAnchor } from './tooltip';

	const DELAY_MS = 400;
	const INFO_DELAY_MS = 150;
	const TIP_ID = 'dy-tooltip-layer';

	let text = $state('');
	let pos = $state<{ top: number; left: number; side: 'top' | 'bottom' } | null>(null);
	let tip = $state<HTMLDivElement | null>(null);

	let anchor: HTMLElement | null = null;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let describedBy: string | null = null;
	// Live cells re-render their title (e.g. memory every second): take the
	// new text over instead of letting the native tooltip show it.
	const watcher =
		typeof MutationObserver === 'undefined'
			? null
			: new MutationObserver(() => {
					const value = anchor?.getAttribute('title');
					if (!anchor || value === null || value === undefined) return;
					anchor.dataset.dyTitle = value;
					anchor.removeAttribute('title');
					if (value.trim()) text = value;
					else hide();
				});

	function restore(el: HTMLElement) {
		const saved = el.dataset.dyTitle;
		delete el.dataset.dyTitle;
		// A re-render may have set a new title meanwhile: keep that one.
		if (saved !== undefined && !el.hasAttribute('title')) el.setAttribute('title', saved);
		if (describedBy === null) el.removeAttribute('aria-describedby');
		else el.setAttribute('aria-describedby', describedBy);
	}

	function hide() {
		clearTimeout(timer);
		timer = undefined;
		watcher?.disconnect();
		if (anchor) restore(anchor);
		anchor = null;
		text = '';
		pos = null;
	}

	async function show(el: HTMLElement) {
		const value = el.getAttribute('title') ?? el.dataset.dyTitle ?? '';
		if (!value.trim() || !el.isConnected) return;
		anchor = el;
		el.dataset.dyTitle = value;
		el.removeAttribute('title');
		describedBy = el.getAttribute('aria-describedby');
		if (el.getAttribute('aria-label') !== value)
			el.setAttribute('aria-describedby', describedBy ? `${describedBy} ${TIP_ID}` : TIP_ID);
		text = value;
		pos = null;
		watcher?.observe(el, { attributes: true, attributeFilter: ['title'] });
		await tick();
		if (anchor !== el || !tip) return;
		const r = el.getBoundingClientRect();
		pos = placeTooltip(
			{ top: r.top, left: r.left, width: r.width, height: r.height },
			{ width: tip.offsetWidth, height: tip.offsetHeight },
			{ width: window.innerWidth, height: window.innerHeight }
		);
	}

	function schedule(el: HTMLElement, delay: number) {
		if (anchor === el) return;
		hide();
		timer = setTimeout(() => void show(el), delay);
	}

	onMount(() => {
		// The info tip a press started on: its release toggles it.
		let pressed: HTMLElement | null = null;
		const over = (e: PointerEvent) => {
			if (e.pointerType === 'touch') return;
			const el = tooltipAnchor(e.target);
			if (el) schedule(el, el.hasAttribute('data-dy-info') ? INFO_DELAY_MS : DELAY_MS);
			else if (anchor || timer) hide();
		};
		const out = (e: PointerEvent) => {
			if (e.pointerType === 'touch') return;
			const next = e.relatedTarget instanceof Node ? e.relatedTarget : null;
			const current = anchor ?? tooltipAnchor(e.target);
			if (current && next && current.contains(next)) return;
			hide();
		};
		const down = (e: PointerEvent) => {
			pressed = infoAnchor(e.target);
			if (!pressed || anchor !== pressed) hide();
		};
		const up = (e: PointerEvent) => {
			const el = infoAnchor(e.target);
			if (!el || el !== pressed) return;
			pressed = null;
			if (anchor !== el) {
				hide();
				void show(el);
			} else if (e.pointerType !== 'mouse') hide();
		};
		const click = (e: MouseEvent) => {
			if (infoAnchor(e.target)) e.preventDefault();
		};
		const focus = (e: FocusEvent) => {
			const el = tooltipAnchor(e.target);
			if (el && el === e.target && el.matches(':focus-visible')) schedule(el, 0);
		};
		// Only the anchor losing focus hides it: a tap moves focus too.
		const blur = (e: FocusEvent) => {
			if (anchor && e.target instanceof Node && e.target.contains(anchor)) hide();
		};
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape' && anchor) hide();
		};
		document.addEventListener('pointerover', over, true);
		document.addEventListener('pointerout', out, true);
		document.addEventListener('pointerdown', down, true);
		document.addEventListener('pointerup', up, true);
		document.addEventListener('click', click, true);
		document.addEventListener('focusin', focus, true);
		document.addEventListener('focusout', blur, true);
		document.addEventListener('keydown', key, true);
		window.addEventListener('scroll', hide, true);
		window.addEventListener('blur', hide);
		return () => {
			hide();
			document.removeEventListener('pointerover', over, true);
			document.removeEventListener('pointerout', out, true);
			document.removeEventListener('pointerdown', down, true);
			document.removeEventListener('pointerup', up, true);
			document.removeEventListener('click', click, true);
			document.removeEventListener('focusin', focus, true);
			document.removeEventListener('focusout', blur, true);
			document.removeEventListener('keydown', key, true);
			window.removeEventListener('scroll', hide, true);
			window.removeEventListener('blur', hide);
		};
	});
</script>

{#if text}
	<div
		bind:this={tip}
		id={TIP_ID}
		role="tooltip"
		class="dy-tooltip layer"
		data-side={pos?.side}
		style:top="{pos?.top ?? 0}px"
		style:left="{pos?.left ?? 0}px"
		style:visibility={pos ? 'visible' : 'hidden'}
	>
		{text}
	</div>
{/if}

<style>
	.layer {
		position: fixed;
		white-space: pre-line;
		overflow-wrap: anywhere;
		pointer-events: none;
		animation: dy-tooltip-in var(--duration-fast) var(--ease-out);
	}

	@keyframes dy-tooltip-in {
		from {
			opacity: 0;
		}
		to {
			opacity: 1;
		}
	}
</style>
