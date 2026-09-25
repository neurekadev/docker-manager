# PWA: install, update and offline

DockYard's web UI is an installable progressive web app (PWA). It needs
the HTTPS origin of your deployment (browsers only install and run service
workers in a secure context).

## Install

| Platform | How |
| --- | --- |
| Chrome, Edge (desktop) | the install icon in the address bar, or menu → *Install DockYard* |
| Android (Chrome) | menu → *Install app* / *Add to home screen* |
| iOS / iPadOS (Safari 16.4+) | Share → *Add to Home Screen* |
| Firefox (desktop) | no install; use it as a normal tab |

The installed app opens in its own window on the same origin and shares
your sign-in with the browser. Supported browsers and versions are in the
[support matrix](../support-matrix.md#browsers).

## Updates

After DockYard is upgraded, the next page load downloads the new app in the
background. Nothing reloads on its own: the **App update** notice offers
**Reload to update** or **Later**. While something would be lost by a
reload (an unsaved editor buffer, an open terminal, an upload or a restore
in progress), the notice says what is still open and waits until it is
done. Reload when convenient; the API and the app are always served from
the same manager, so an old tab keeps working until you reload.

## Offline

DockYard deliberately stores no data offline (#25 Q4):

- The app shell (HTML, scripts, styles, icons) is cached, so the app opens
  and shows a clear **offline** notice when the manager cannot be reached.
- API responses are never cached by the service worker or kept in browser
  storage (they are sent with `Cache-Control: no-store`), so nothing about
  your hosts stays on a lost laptop or phone beyond what is on screen.
- Actions are never queued: while offline, buttons that change something
  fail with a message instead of running later.
- When the connection returns, the app reconnects its live stream,
  refetches what changed and removes the notice.

The only thing kept locally is the environment you selected in the
switcher (its ID, per user), so the app opens where you left it.

## Live updates

Open views update live through one stream per tab: containers starting or
stopping, jobs progressing, stack revisions, files changed on the host
(visible within about two seconds on local disks, within a minute on
network filesystems), other users' changes. What reaches a view is filtered
by your permissions; when your permissions change, the stream is closed and
reopened with the new rules. An editor with unsaved changes is never
overwritten: if the file changed on disk, saving is refused and you choose
to reload or overwrite.

## Troubleshooting the app

- "Service worker registration failed": you opened DockYard over plain
  HTTP or an untrusted certificate. Use the HTTPS origin and trust its CA.
- The app looks outdated after an upgrade: accept **Reload to update**, or
  close all DockYard tabs and windows and open it again.
- Passkeys stopped working after a host name change: passkeys are bound to
  the host name; sign in with password (and TOTP or a recovery code) and
  register a new passkey.
