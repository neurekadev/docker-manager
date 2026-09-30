import Link from 'next/link';
import { BookOpen, Images } from 'lucide-react';
import { appName, docsRoute } from '@/lib/shared';

export default function Home() {
  return (
    <main className="relative flex flex-1 flex-col items-center justify-center overflow-hidden px-6 py-16 text-center">
      {/* Soft glow in the logo's blue behind the logo. */}
      <div
        aria-hidden
        className="pointer-events-none absolute left-1/2 top-1/2 -z-10 size-[36rem] -translate-x-1/2 -translate-y-[60%] rounded-full bg-fd-primary/10 blur-3xl"
      />
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img src="/logo-512.png" alt="" width={176} height={176} className="size-36 sm:size-44" />
      <h1 className="mt-6 text-4xl font-semibold tracking-tight sm:text-5xl">{appName}</h1>
      <p className="mt-4 max-w-xl text-xl text-balance sm:text-2xl">
        One pane of glass for your entire Docker infrastructure.
      </p>
      <p className="mt-3 max-w-lg text-balance text-fd-muted-foreground">
        Deploy, edit, update and back up every server from your browser. No SSH, no text editor.
      </p>
      <nav className="mt-10 grid w-full max-w-md gap-3 sm:grid-cols-2">
        <Link
          href={docsRoute}
          className="flex items-center justify-center gap-2 rounded-xl bg-fd-primary px-6 py-3 font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
        >
          <BookOpen className="size-5" aria-hidden />
          Documentation
        </Link>
        <Link
          href="/screenshots"
          className="flex items-center justify-center gap-2 rounded-xl border border-fd-border bg-fd-card px-6 py-3 font-medium transition-colors hover:bg-fd-accent"
        >
          <Images className="size-5" aria-hidden />
          Screenshots
        </Link>
      </nav>
    </main>
  );
}
