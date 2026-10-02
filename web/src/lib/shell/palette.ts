// Command palette model (#22 ⌘K): recent pages, quick actions, pages and
// search hits (GET /api/v1/search) become one flat, keyboard-navigable
// result list.
import History from '@lucide/svelte/icons/history';
import type { SearchHit, SearchResults } from '$lib/api/client';
import type { IconComponent } from '$lib/design/icons';
import { RESOURCE_ICONS } from '$lib/features/common/resourceIcons';
import { routes } from '$lib/routes';
import { activeNav, hasAny, type Access, type NavItem } from './nav';
import type { RecentPage } from './recent.svelte';

export interface PaletteResult {
	id: string;
	group: string;
	label: string;
	secondary?: string;
	icon: IconComponent;
	href: string;
}

const TYPE: Record<SearchHit['type'], { group: string; icon: IconComponent }> = {
	environment: { group: 'Environments', icon: RESOURCE_ICONS.environment.icon },
	stack: { group: 'Stacks', icon: RESOURCE_ICONS.stack.icon },
	service: { group: 'Services', icon: RESOURCE_ICONS.service.icon },
	container: { group: 'Containers', icon: RESOURCE_ICONS.container.icon },
	image: { group: 'Images', icon: RESOURCE_ICONS.image.icon },
	volume: { group: 'Volumes', icon: RESOURCE_ICONS.volume.icon },
	network: { group: 'Networks', icon: RESOURCE_ICONS.network.icon }
};

/** The page a search hit opens. */
export function hrefForHit(hit: SearchHit): string {
	const env = hit.environmentId ?? '';
	switch (hit.type) {
		case 'environment':
			return routes.environment(hit.id);
		case 'stack':
			return routes.stack(hit.id);
		case 'service':
			return `${routes.stack(hit.stackId ?? hit.id.split('/')[0])}?service=${encodeURIComponent(hit.name)}`;
		// Containers and networks are addressed by name (#17 identity, #23 keys).
		case 'container':
			return routes.container(env, hit.name);
		case 'image':
			return routes.image(env, hit.id);
		case 'volume':
			return routes.volume(env, hit.id);
		case 'network':
			return routes.network(env, hit.name);
	}
}

/** Pages whose label (or keywords: "password" finds Profile) contain the query. */
export function pageResults(items: NavItem[], q: string): PaletteResult[] {
	const needle = q.trim().toLowerCase();
	return items
		.filter(
			(i) =>
				!needle ||
				i.label.toLowerCase().includes(needle) ||
				!!i.keywords?.toLowerCase().includes(needle)
		)
		.map((i) => ({
			id: `page:${i.id}`,
			group: 'Pages',
			label: i.label,
			icon: i.icon,
			href: i.href
		}));
}

/**
 * "Recent": the last visited pages (not the current one), newest first.
 * Entries are named by their page title (known in this tab) or, for
 * section pages, by the section; others are left out.
 */
export function recentResults(
	recent: readonly RecentPage[],
	pages: NavItem[],
	current?: string,
	limit = 5
): PaletteResult[] {
	const out: PaletteResult[] = [];
	for (const r of recent) {
		if (out.length >= limit) break;
		if (r.path === current) continue;
		const section = activeNav(r.path, pages);
		const label = r.title ?? (section?.href === r.path ? section.label : undefined);
		if (!label) continue;
		out.push({
			id: `recent:${r.path}`,
			group: 'Recent',
			label,
			secondary: section && section.href !== r.path ? section.label : undefined,
			icon: section?.icon ?? History,
			href: r.path
		});
	}
	return out;
}

interface PaletteAction {
	id: string;
	label: string;
	icon: IconComponent;
	/** Shown to callers holding one of these capabilities somewhere. */
	capabilities: string[];
	href: (environmentId: string | null) => string;
}

/** Quick actions: the create flows people start most, each where it lives. */
export const PALETTE_ACTIONS: PaletteAction[] = [
	{
		id: 'create-stack',
		label: 'Create Stack',
		icon: RESOURCE_ICONS.stack.icon,
		capabilities: ['stack.create'],
		href: (env) => routes.newStack(env)
	},
	{
		id: 'create-container',
		label: 'Create Container',
		icon: RESOURCE_ICONS.container.icon,
		capabilities: ['container.create'],
		href: (env) => routes.newContainer(env ?? undefined)
	},
	{
		id: 'build-image',
		label: 'Build Image',
		icon: RESOURCE_ICONS.build.icon,
		capabilities: ['image.build'],
		href: (env) => routes.newBuild(env ?? undefined)
	},
	{
		id: 'add-environment',
		label: 'Add Environment',
		icon: RESOURCE_ICONS.environment.icon,
		capabilities: ['agent.enroll'],
		href: () => routes.addEnvironment()
	}
];

/**
 * "Actions" the caller may start (hidden, not disabled, without the
 * capability; the server still decides), filtered by the query.
 */
export function actionResults(
	access: Access | undefined,
	environmentId: string | null,
	q = ''
): PaletteResult[] {
	if (!access) return [];
	const needle = q.trim().toLowerCase();
	return PALETTE_ACTIONS.filter(
		(a) =>
			hasAny(access, ...a.capabilities) && (!needle || a.label.toLowerCase().includes(needle))
	).map((a) => ({
		id: `action:${a.id}`,
		group: 'Actions',
		label: a.label,
		icon: a.icon,
		href: a.href(environmentId)
	}));
}

export function hitResults(res: SearchResults | undefined): PaletteResult[] {
	if (!res) return [];
	return res.items.map((h) => {
		const t = TYPE[h.type];
		const where = [h.environmentName, h.status].filter(Boolean).join(', ');
		return {
			id: `${h.type}:${h.environmentId ?? ''}:${h.id}`,
			group: t.group,
			label: h.name,
			secondary: where || undefined,
			icon: t.icon,
			href: hrefForHit(h)
		};
	});
}

/** Groups in display order, keeping each group's result order. */
export function grouped(results: PaletteResult[]): { group: string; items: PaletteResult[] }[] {
	const out: { group: string; items: PaletteResult[] }[] = [];
	for (const r of results) {
		let g = out.find((x) => x.group === r.group);
		if (!g) out.push((g = { group: r.group, items: [] }));
		g.items.push(r);
	}
	return out;
}
