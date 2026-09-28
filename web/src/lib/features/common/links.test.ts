import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
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
		expect(screen.getByRole('group', { name: 'Links' })).toBeInTheDocument();
		expect(screen.getByLabelText('Label of link 1')).toHaveValue('Docs');

		await user.click(screen.getByRole('button', { name: 'Add link' }));
		await user.type(screen.getByLabelText('URL of link 2'), ' https://git.example.com/app ');
		expect(saved()).toEqual([
			{ label: 'Docs', url: 'https://docs.example.com' },
			{ url: 'https://git.example.com/app' }
		]);

		await user.click(screen.getByRole('button', { name: 'Remove link 1' }));
		expect(saved()).toEqual([{ url: 'https://git.example.com/app' }]);
		expect(screen.getByLabelText('URL of link 1')).toHaveValue(' https://git.example.com/app ');
	});

	it('says what is wrong once a field is left, as the server would', async () => {
		const user = setup();
		render(LinksEditorHarness, { props: {} });
		await user.click(screen.getByRole('button', { name: 'Add link' }));
		const url = screen.getByLabelText('URL of link 1');
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
		expect(screen.getByLabelText('URL of link 2')).toHaveAccessibleDescription(
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
		expect(screen.getByLabelText('URL of link 1')).toHaveAccessibleDescription(
			'Must not contain a user name or password.'
		);
	});

	it('offers "Add link" only below 10 links', () => {
		render(LinksEditorHarness, {
			props: {
				initial: Array.from({ length: 10 }, (_, i) => ({ url: `https://example.com/${i}` }))
			}
		});
		expect(screen.queryByRole('button', { name: 'Add link' })).not.toBeInTheDocument();
		expect(screen.getAllByRole('button', { name: /^Remove link/ })).toHaveLength(10);
	});
});
