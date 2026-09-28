import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import KeyRound from '@lucide/svelte/icons/key-round';
import Layers from '@lucide/svelte/icons/layers';
import Workflow from '@lucide/svelte/icons/workflow';
import { TILE_COLORS } from '$lib/design/hue';
import { NAV_ITEMS } from '$lib/shell/nav';
import NameCell from './NameCell.svelte';
import {
	RESOURCE_ICONS,
	environmentIcon,
	resourceIcon,
	scheduleResource,
	type ResourceKind
} from './resourceIcons';

describe('resource icons (#22 list rows)', () => {
	it('gives every resource type an icon and a tile colour', () => {
		for (const kind of Object.keys(RESOURCE_ICONS) as ResourceKind[]) {
			const r = resourceIcon(kind);
			expect(r.icon, kind).toBeTruthy();
			expect(TILE_COLORS, kind).toContain(r.color);
		}
	});

	it('shares the type icon with the sidebar section', () => {
		const icon = (id: string) => NAV_ITEMS.find((i) => i.id === id)?.icon;
		expect(icon('containers')).toBe(RESOURCE_ICONS.container.icon);
		expect(icon('volumes')).toBe(RESOURCE_ICONS.volume.icon);
		expect(icon('jobs')).toBe(RESOURCE_ICONS.job.icon);
		expect(icon('updates')).toBe(RESOURCE_ICONS.updatePolicy.icon);
		expect(icon('registries')).toBe(RESOURCE_ICONS.registry.icon);
	});

	it('shows registry connections with the key icon, apart from API tokens by colour', () => {
		expect(RESOURCE_ICONS.registry.icon).toBe(KeyRound);
		expect(RESOURCE_ICONS.apiToken.icon).toBe(KeyRound);
		expect(RESOURCE_ICONS.registry.color).not.toBe(RESOURCE_ICONS.apiToken.color);
	});

	it('gives stacks and services one icon each (they have none of their own)', () => {
		expect(RESOURCE_ICONS.stack).toEqual({ icon: Layers, color: 'blue' });
		expect(RESOURCE_ICONS.service).toEqual({ icon: Workflow, color: 'blue' });
	});

	it('shows an offline environment in slate', () => {
		expect(environmentIcon(true)).toEqual(RESOURCE_ICONS.environment);
		expect(environmentIcon(false)).toEqual({
			icon: RESOURCE_ICONS.environment.icon,
			color: 'slate'
		});
	});

	it('shows a schedule with the icon of the policy it runs', () => {
		expect(scheduleResource('backup')).toBe('backupPolicy');
		expect(scheduleResource('backup_verification')).toBe('backupRepository');
		expect(scheduleResource('update_check')).toBe('updatePolicy');
		expect(scheduleResource('update_run')).toBe('updatePolicy');
		expect(scheduleResource('prune')).toBe('maintenancePolicy');
		expect(scheduleResource('something_new')).toBe('schedule');
	});
});

describe('NameCell with a row icon', () => {
	it('puts a decorative tile in the type colour before the name link', () => {
		const { container } = render(NameCell, {
			props: {
				name: 'nightly',
				href: '/backups/policies/p1',
				sub: 'Manager',
				icon: 'backupPolicy'
			}
		});
		const tile = container.querySelector('[data-color]');
		expect(tile).toHaveAttribute('aria-hidden', 'true');
		expect(tile).toHaveAttribute('data-color', 'teal');
		expect(tile?.classList.contains('xs')).toBe(true);
		const link = screen.getByRole('link', { name: 'nightly' });
		expect(link).toHaveAttribute('href', '/backups/policies/p1');
		// The icon comes first; the name stays the only text of the link.
		expect(tile!.compareDocumentPosition(link) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
		expect(screen.getByText('Manager')).toBeInTheDocument();
	});

	it('takes a colour override (an offline environment, a protected container)', () => {
		const { container } = render(NameCell, {
			props: { name: 'homelab', icon: 'environment', iconColor: 'slate' }
		});
		expect(container.querySelector('[data-color]')).toHaveAttribute('data-color', 'slate');
	});

	it('renders no tile without an icon', () => {
		const { container } = render(NameCell, { props: { name: 'plain' } });
		expect(container.querySelector('[data-color]')).toBeNull();
		expect(screen.getByText('plain')).toBeInTheDocument();
	});
});
