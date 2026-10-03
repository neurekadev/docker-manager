# User documentation (`docs/public`)

Binding conventions (split out of CLAUDE.md). Read this file whenever a
change alters what users see or do, and before you touch
`docs/public/content/docs`.

The user documentation is the product's only manual and install guide. It
is short on purpose: a user should find the one thing they need, do it, and
leave. Docker Manager is a single pane of glass: everything is done in the
app, so the docs describe the app, not the shell.

## When you must update it (same commit, no exceptions)

A change is not done until the user docs match it. Update them in the same
commit when you:

- add, rename or remove a UI label, page, tab, menu entry, button, dialog
  field or flow a page names;
- change a default, limit, range, duration or count a page states;
- add, rename or remove a configuration variable: the Configuration page
  lists **every** variable the manager and the agent read (including the
  advanced ones and `DOCKER_HOST`), with its default and range;
- change requirements, the install or upgrade steps, the compose files or
  a message users must act on (error codes in Troubleshooting);
- add a major feature (a new sidebar entry): it gets a page under
  **Features**. Removing a feature removes its page or section.

Before you commit, search the docs for every label, variable and number
you changed (`grep -rn "<old label>" docs/public/content`).

`scripts/policy-check.sh` (CI lint) fails when a configuration variable is
missing from the Configuration page, a page names a variable the code no
longer reads, a docs link or `#anchor` is broken, or `meta.json` and the
page files differ. It can't check labels or behavior: that is on you.

## Verify every statement

Nothing goes in unverified. Before you write or keep a sentence:

- **Labels**: find the exact string in `web/src` (case, punctuation, curly
  apostrophes, `…`). Write it as the app shows it.
- **Numbers and defaults**: read them in the Go or TypeScript code
  (`internal/manager/config`, `internal/agent/config`, the feature's
  package), never in older docs or internal notes alone.
- **Behavior**: read the code path. If you can't confirm a sentence, leave
  it out.
- Never copy a claim from older docs without checking it again.

## Structure (fixed)

The sidebar (`meta.json`) has five sections, in this order. Keep this set;
add a page only for a new major feature, and never add pages for the sake
of having them. `meta.json` lists only pages that exist (the policy check
fails otherwise), so a planned page joins its section in the PR that adds
it.

| Section | Pages |
| --- | --- |
| Getting started | `overview`, `quickstart` (install, add more servers, upgrade) |
| Features | `environments`, `stacks` (with import and rename), `containers`, `images`, `volumes`, `networks`, `builds`, `templates`, `registries` (with Git credentials), `backups` (with restore and recovery), `updates`, `maintenance` (one-off prunes and maintenance settings), `file-manager`, `terminal`, `logs`, `migrations` (with moving Docker Manager) |
| Monitoring and notifications | `metrics` (server and container charts), `monitoring` (disk health and RAID, disk access), `alerts` (what raises an alert, see and dismiss alerts, how they reach your channels), `notifications` (notification channels: add, test, view or change the address, what to send) |
| Administration | `users-and-groups` (people, groups, your account, sign-in policy), `api-tokens`, `permissions`, `audit-log` |
| Help | `configuration` (every variable), `troubleshooting` |

Each fact lives on one page; other pages link to it instead of repeating it.

## Page shape

1. Frontmatter: `title` is the sidebar name, `description` is one sentence.
2. Open with one to three sentences on what the feature does for the user.
3. Task sections named after what the user does (`## Create a stack`), with
   numbered steps in the order the user clicks.
4. An optional short **Good to know** list (at most five bullets) for
   limits and edge cases users will hit.

Keep pages lean: most feature pages fit in 40 to 100 lines of MDX. If a page
grows past that, cut explanation before you cut steps.

## Writing rules

- Write for people who run Docker but are not developers: plain words,
  short sentences, active voice, "you".
- UI elements in bold, exactly as shown, in click order: **Stacks → Create
  stack**.
- One clear example per task. Commands and YAML are complete and
  copy-ready; mark kept lines with `# ...keep the existing lines...`. Use
  `docker.example.com` as the address.
- Prefer the app over the shell. Show a terminal step only where the app
  has no way to do it (installing, agent settings, recovery), and say so.
- Callouts only for data loss, security, or a step users must not skip; at
  most two per page.
- Links are absolute from the site's root: `/<page>` or `/<page>#<anchor>`
  (for example `/quickstart#upgrade`). Never add the base path.

## Never include

- Internals: API paths, job kinds, package or file names of this repo,
  capability keys, issue numbers, architecture, how something works inside.
- Error codes outside Troubleshooting.
- Over-explaining: background, rationale, history, every option of a form.
  Name only the fields users must decide on.
- Plans, "coming soon", changelogs, marketing claims.
- Real host names, IP addresses, tokens or any secret.

## Install files

The `compose.yaml` and `.env` in the Quickstart (install and "Add more
servers") are the deployment. Keep them in sync with the install commands
and the move files (`internal/manager/agents/install.go`) and with
`docs/internal/deployment.md`. No proxy examples, no `deploy/` examples.

## The site

`docs/public` is a Fumadocs static Next.js export, dark only, with local
search, served by nginx on port 3000 (`docs/public/Dockerfile`,
`nginx.conf`; `absolute_redirect off` keeps redirects relative so a reverse
proxy's host and port survive). The landing page (`app/page.tsx`) links to
the docs at `/overview/` and to the Screenshots page (`app/screenshots`). Docs
pages live at the site's root, `/<page>/` (route group `app/(docs)`).
`.github/workflows/Docs.yaml` builds the image and publishes
`ghcr.io/neurekadev/docker-manager-docs:edge` on pushes to
`main` that change `docs/public/**`.

The published site lives under a path, `https://docs.neureka.dev/docker-manager`:
the build argument `DOCS_BASE_PATH=/docker-manager` (Docs.yaml) sets
Next's `basePath`, puts the files under that path in the image and fills
it into `nginx.conf` (`__BASE__`; `/` redirects there). The reverse proxy
passes the path through unchanged; a prefix-stripping rewrite can't work,
because the pages link their assets by absolute paths. Next adds the prefix
to `next/link` links and its assets; write every other root-absolute path
(images, files in `public/`, the search index) through `withBase()` from
`lib/shared.ts`.

Link previews (Discord, Slack, X) use one source of truth in `lib/shared.ts`
and `lib/metadata.ts`, and every page sets them through `shareMetadata()`
(a page's own `openGraph` replaces its parent's):

- Share image: the logo (`/logo-512.png`, 512×512, `twitter:card`
  `summary`) as an absolute URL. `DOCS_SITE_URL=https://docs.neureka.dev`
  (Docs.yaml) sets `metadataBase`; the helper adds the base path.
- Strip color: `viewport.themeColor` = `brandColor`, the logo's blue, the
  same value as `--color-fd-primary` in `app/global.css`.
- Text: the site's title is the app name; its description is exactly the
  landing page's `tagline`, a capitalized sentence ending in a period,
  never prefixed with the app name ("Docker Manager: ...") and never
  lowercased. Docs pages use their own title and `description`.

Check the tags in the built HTML (`og:image`, `twitter:image`,
`theme-color`, `og:description`) after changing them. For a local check, build with
`DOCS_BASE_PATH=/docker-manager DOCS_SITE_URL=https://docs.neureka.dev` (in Git
Bash also `MSYS_NO_PATHCONV=1`).

Screenshots live in `docs/public/public/screenshots/<desktop|tablet|mobile>/<page>.webp`,
listed in `docs/public/lib/screenshots.ts` in the order of the app's side menu
(the Screenshots page shows one carousel each for desktop, tablet and mobile).
Take them from a fresh, throwaway instance that holds only the Docker
Manager stack and one small demo app with a shell (for the terminal and
logs), never real data, at 1440×900 (desktop), 820×1180 (tablet) and
390×844 (mobile), as WebP. Retake the affected ones when a page's layout
changes.

Build the site locally (`npm --prefix docs/public ci && npm --prefix
docs/public run build`) only after changing its code or structure, not for
text edits.
