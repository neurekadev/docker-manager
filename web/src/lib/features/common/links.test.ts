import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import LinksEditorHarness from '../../../test/LinksEditorHarness.svelte';
import LinkList from './LinkList.svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const saved = () => JSON.parse(screen.getByTestId('links').textContent ?? '[]');

describe('LinkList', () => {
	it('shows each link as an external link: label or host, new tab, no referrer', () => {
		render(LinkList, {
			props: {
				label: 'Links of Silo',
				links: [
					{ label: 'Documentation', url: 'https://docs.example.com/silo' },
					{ url: 'https://git.example.com/silo?tab=readme' }
				]
			}
		});
		const list = screen.getByRole('list', { name: 'Links of Silo' });
		const links = within(list).getAllByRole('link');
		expect(links).toHaveLength(2);
		expect(links[0]).toHaveAccessibleName('Documentation (opens in a new tab)');
		expect(links[0]).toHaveAttribute('href', 'https://docs.example.com/silo');
		expect(links[1]).toHaveAccessibleName('git.example.com (opens in a new tab)');
		for (const a of links) {
			expect(a).toHaveAttribute('target', '_blank');
			expect(a).toHaveAttribute('rel', 'noopener noreferrer');
		}
		expect(links[1]).toHaveAttribute('title', 'https://git.example.com/silo?tab=readme');
	});

	it('renders nothing without links', () => {
		render(LinkList, { props: { links: [] } });
		expect(screen.queryByRole('list')).not.toBeInTheDocument();
	});
});

describe('LinksEditor', () => {
	it('adds, edits and removes rows and saves trimmed links', async () => {
		const user = setup();
		render(LinksEditorHarness, {
			props: { initial: [{ label: 'Docs', url: 'https://docs.example.com' }] }
		});
		const group = screen.getByRole('group', { name: 'Links' });
		// What the links are for sits behind an (i), outside the group's name.
		expect(
			screen.getByRole('img', {
				name: 'Pages such as the documentation, website or repository, shown on the page.'
			})
		).toBeInTheDocument();
		expect(group).toHaveAccessibleDescription(
			'Pages such as the documentation, website or repository, shown on the page.'
		);
		expect(screen.queryByText(/^Optional\./)).not.toBeInTheDocument();
		expect(screen.getByLabelText('Label of Link 1')).toHaveValue('Docs');

		await user.click(screen.getByRole('button', { name: 'Add Link' }));
		await user.type(screen.getByLabelText('URL of Link 2'), ' https://git.example.com/app ');
		expect(saved()).toEqual([
			{ label: 'Docs', url: 'https://docs.example.com' },
			{ url: 'https://git.example.com/app' }
		]);

		await user.click(screen.getByRole('button', { name: 'Remove Link 1' }));
		expect(saved()).toEqual([{ url: 'https://git.example.com/app' }]);
		expect(screen.getByLabelText('URL of Link 1')).toHaveValue(' https://git.example.com/app ');
	});

	it('says what is wrong once a field is left, as the server would', async () => {
		const user = setup();
		render(LinksEditorHarness, { props: {} });
		await user.click(screen.getByRole('button', { name: 'Add Link' }));
		const url = screen.getByLabelText('URL of Link 1');
		await user.type(url, 'javascript:alert(1)');
		// Not while typing.
		expect(url).not.toHaveAccessibleDescription(/http/);
		await user.tab();
		expect(url).toHaveAccessibleDescription(
			'Use a web address that starts with http:// or https://.'
		);
		expect(url).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByTestId('valid')).toHaveTextContent('invalid');

		await user.clear(url);
		await user.type(url, 'https://me:secret@example.com');
		expect(url).toHaveAccessibleDescription(
			'Remove the user name and password from the address.'
		);
	});

	it('shows every problem after a save attempt and the server’s answer', () => {
		render(LinksEditorHarness, {
			props: {
				initial: [
					{ url: 'https://docs.example.com' },
					{ label: 'Again', url: 'https://docs.example.com' }
				],
				showAll: true
			}
		});
		expect(screen.getByLabelText('URL of Link 2')).toHaveAccessibleDescription(
			'This address is already listed.'
		);
	});

	it('shows the server’s problem on the row it names', () => {
		render(LinksEditorHarness, {
			props: {
				initial: [{ url: 'https://docs.example.com' }],
				serverProblems: {
					rows: [{ url: 'Must not contain a user name or password.' }],
					list: null
				}
			}
		});
		expect(screen.getByLabelText('URL of Link 1')).toHaveAccessibleDescription(
			'Must not contain a user name or password.'
		);
	});

	it('reorders rows with the keyboard on the grip, the row’s state moving with it', async () => {
		const user = setup();
		render(LinksEditorHarness, {
			props: {
				initial: [
					{ label: 'Docs', url: 'https://docs.example.com' },
					{ label: 'Code', url: 'https://git.example.com' }
				]
			}
		});
		// A problem shown on the second row (the field was left).
		const url2 = screen.getByLabelText('URL of Link 2');
		await user.clear(url2);
		await user.type(url2, 'ftp://git.example.com');
		await user.tab();
		expect(url2).toHaveAttribute('aria-invalid', 'true');

		screen.getByRole('button', { name: 'Reorder Link 2' }).focus();
		await user.keyboard('{ArrowUp}');
		expect(saved()).toEqual([
			{ label: 'Code', url: 'ftp://git.example.com' },
			{ label: 'Docs', url: 'https://docs.example.com' }
		]);
		expect(screen.getByLabelText('Label of Link 1')).toHaveValue('Code');
		expect(screen.getByLabelText('URL of Link 1')).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByLabelText('URL of Link 2')).not.toHaveAttribute('aria-invalid');
		expect(screen.getByRole('status')).toHaveTextContent('Moved Link 2 to position 1 of 2.');
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Reorder Link 1' })).toHaveFocus()
		);

		// Already first: ArrowUp changes nothing; End moves it last.
		await user.keyboard('{ArrowUp}');
		expect(screen.getByLabelText('Label of Link 1')).toHaveValue('Code');
		await user.keyboard('{End}');
		expect(screen.getByLabelText('Label of Link 2')).toHaveValue('Code');
	});

	it('reorders rows by dragging the grip', async () => {
		render(LinksEditorHarness, {
			props: {
				initial: [
					{ label: 'Docs', url: 'https://docs.example.com' },
					{ label: 'Code', url: 'https://git.example.com' }
				]
			}
		});
		const group = screen.getByRole('group', { name: 'Links' });
		// Rows 40 px tall, 8 px apart (jsdom has no layout).
		within(group)
			.getAllByRole('listitem')
			.forEach((li, i) => {
				const top = i * 48;
				vi.spyOn(li, 'getBoundingClientRect').mockReturnValue({
					top,
					bottom: top + 40,
					height: 40,
					left: 0,
					right: 600,
					width: 600,
					x: 0,
					y: top,
					toJSON: () => ({})
				} as DOMRect);
			});
		const grip = screen.getByRole('button', { name: 'Reorder Link 1' });
		await fireEvent.pointerDown(grip, { button: 0, pointerId: 1, clientY: 20 });
		await fireEvent.pointerMove(grip, { pointerId: 1, clientY: 80 });
		await fireEvent.pointerUp(grip, { pointerId: 1, clientY: 80 });
		expect(saved()).toEqual([
			{ label: 'Code', url: 'https://git.example.com' },
			{ label: 'Docs', url: 'https://docs.example.com' }
		]);

		// Escape cancels a drag.
		const grip2 = screen.getByRole('button', { name: 'Reorder Link 1' });
		await fireEvent.pointerDown(grip2, { button: 0, pointerId: 2, clientY: 20 });
		await fireEvent.pointerMove(grip2, { pointerId: 2, clientY: 80 });
		await fireEvent.keyDown(window, { key: 'Escape' });
		await fireEvent.pointerUp(grip2, { pointerId: 2, clientY: 80 });
		expect(screen.getByLabelText('Label of Link 1')).toHaveValue('Code');
	});

	it('offers "Add Link" only below 10 links', () => {
		render(LinksEditorHarness, {
			props: {
				initial: Array.from({ length: 10 }, (_, i) => ({ url: `https://example.com/${i}` }))
			}
		});
		expect(screen.queryByRole('button', { name: 'Add Link' })).not.toBeInTheDocument();
		expect(screen.getAllByRole('button', { name: /^Remove Link/ })).toHaveLength(10);
	});
});
