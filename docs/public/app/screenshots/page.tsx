import { existsSync } from 'node:fs';
import { join } from 'node:path';
import type { Metadata } from 'next';
import Link from 'next/link';
import { ArrowLeft } from 'lucide-react';
import { ScreenshotGallery } from '@/components/screenshot-gallery';
import { docsRoute } from '@/lib/shared';
import { devices, screenshots, screenshotSrc } from '@/lib/screenshots';

export const metadata: Metadata = { title: 'Screenshots' };

// Built once (static export): only features with an image for every screen
// size are shown.
function available() {
  const root = join(process.cwd(), 'public');
  return screenshots.filter((s) => devices.every((d) => existsSync(join(root, screenshotSrc(s.slug, d.id)))));
}

export default function Screenshots() {
  const items = available();

  return (
    <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col items-center px-6 py-16 text-center">
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src="/logo-512.png" alt="" width={96} height={96} className="size-24" />
      <h1 className="mt-6 text-3xl font-semibold tracking-tight">Screenshots</h1>
      {items.length > 0 ? (
        <>
          <p className="mt-3 max-w-xl text-lg text-fd-muted-foreground">
            Every server, stack and container in one place, on any screen.
          </p>
          <ScreenshotGallery items={items} />
        </>
      ) : (
        <>
          <p className="mt-3 text-lg text-fd-muted-foreground">Coming soon.</p>
          <p className="mt-1 text-fd-muted-foreground">
            Until then, the{' '}
            <Link href={docsRoute} className="text-fd-primary underline-offset-4 hover:underline">
              docs
            </Link>{' '}
            walk you through every feature.
          </p>
        </>
      )}
      <Link
        href="/"
        className="mt-12 inline-flex items-center gap-2 rounded-xl border border-fd-border bg-fd-card px-5 py-2.5 font-medium transition-colors hover:bg-fd-accent"
      >
        <ArrowLeft className="size-4" aria-hidden />
        Back
      </Link>
    </main>
  );
}
