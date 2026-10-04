<script lang="ts">
	// A fenced code block of the Markdown preview, highlighted like the
	// editor (highlightCode in $lib/lazy; the classes are coloured in
	// MarkdownView). Plain text until the language's parser has loaded.
	import { highlightCode, languageByName, type CodeSpan } from '$lib/lazy';

	let { text, language }: { text: string; language: string } = $props();

	const NL = '\n';
	let lines = $state<CodeSpan[][] | null>(null);
	$effect(() => {
		const l = languageByName(language);
		const code = text;
		let current = true;
		lines = null;
		if (l && l !== 'text')
			highlightCode(code, l)
				.then((out) => current && (lines = out))
				.catch(() => {
					// Stays plain text.
				});
		return () => (current = false);
	});
</script>

<pre class="code"><code
		>{#if lines}{#each lines as line, i (i)}{#if i > 0}{NL}{/if}{#each line as span, j (j)}{#if span.classes}<span
							class={span.classes}>{span.text}</span
						>{:else}{span.text}{/if}{/each}{/each}{:else}{text}{/if}</code
	></pre>
