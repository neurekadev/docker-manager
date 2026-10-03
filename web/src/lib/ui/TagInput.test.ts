import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import TagInput from './TagInput.svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const input = () => screen.getByRole('textbox', { name: 'Tags' });
const chips = () => {
	const list = screen.queryByRole('list', { name: 'Current Tags' });
	return list
		? within(list)
				.getAllByRole('listitem')
				.map((li) => li.textContent?.trim())
		: [];
};

describe('TagInput', () => {
	it('labels the input and ends a tag on Enter, Space and a comma', async () => {
		const user = setup();
		render(TagInput, { props: { label: 'Tags', description: 'Optional.' } });
		expect(input()).toHaveAccessibleDescription('Optional.');
		await user.type(input(), 'web{Enter}db cache,');
		expect(chips()).toEqual(['web', 'db', 'cache']);
		expect(input()).toHaveValue('');
	});

	it('splits pasted text into several tags', async () => {
		const user = setup();
		render(TagInput, { props: { label: 'Tags' } });
		await user.click(input());
		await user.paste('a, b c');
		expect(chips()).toEqual(['a', 'b', 'c']);
		expect(input()).toHaveValue('');
	});

	it('ignores duplicates and applies normalize', async () => {
		const user = setup();
		render(TagInput, {
			props: {
				label: 'Tags',
				values: ['web'],
				normalize: (s: string) => s.trim().toLowerCase()
			}
		});
		await user.type(input(), 'Web WEB db ');
		expect(chips()).toEqual(['web', 'db']);
	});

	it('removes a tag with its × and the last one with Backspace', async () => {
		const user = setup();
		render(TagInput, { props: { label: 'Tags', values: ['web', 'db', 'cache'] } });
		await user.click(screen.getByRole('button', { name: 'Remove db' }));
		expect(chips()).toEqual(['web', 'cache']);
		expect(input()).toHaveFocus();
		await user.keyboard('{Backspace}');
		expect(chips()).toEqual(['web']);
		// Backspace edits typed text before it removes tags.
		await user.type(input(), 'ab{Backspace}');
		expect(input()).toHaveValue('a');
		expect(chips()).toEqual(['web']);
	});

	it('keeps typed text as a tag when the field is left', async () => {
		const user = setup();
		render(TagInput, { props: { label: 'Tags' } });
		await user.type(input(), 'api');
		await user.click(document.body);
		expect(chips()).toEqual(['api']);
		expect(input()).toHaveValue('');
	});

	it('marks refused tags and says why', async () => {
		const user = setup();
		render(TagInput, {
			props: {
				label: 'Tags',
				validate: (t: string) => (t.includes('_') ? `${t}: use dashes.` : '')
			}
		});
		await user.type(input(), 'ok bad_tag{Enter}');
		expect(chips()).toEqual(['ok', 'bad_tag']);
		expect(input()).toHaveAttribute('aria-invalid', 'true');
		expect(input()).toHaveAccessibleDescription('bad_tag: use dashes.');
		await user.click(screen.getByRole('button', { name: 'Remove bad_tag' }));
		expect(input()).not.toHaveAttribute('aria-invalid');
	});

	it('takes at most max tags and keeps the rest typed', async () => {
		const user = setup();
		render(TagInput, { props: { label: 'Tags', max: 2 } });
		await user.type(input(), 'a b c{Enter}');
		expect(chips()).toEqual(['a', 'b']);
		expect(input()).toHaveValue('c');
		expect(input()).toHaveAccessibleDescription('At most 2 fit. Remove one to add another.');
	});

	it('changes nothing while disabled', () => {
		render(TagInput, { props: { label: 'Tags', values: ['web'], disabled: true } });
		expect(input()).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Remove web' })).toBeDisabled();
	});
});
