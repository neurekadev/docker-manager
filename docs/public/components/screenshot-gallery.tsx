'use client';
import { useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { ArrowRight, ChevronLeft, ChevronRight, Monitor, Smartphone, Tablet } from 'lucide-react';
import { devices, screenshotSrc, type DeviceId, type Screenshot } from '@/lib/screenshots';

const icons = { desktop: Monitor, tablet: Tablet, mobile: Smartphone } satisfies Record<DeviceId, unknown>;

// How wide one slide is: a desktop fills the row, tablets and phones show
// their neighbours.
const slides: Record<DeviceId, string> = {
  desktop: 'w-full',
  tablet: 'w-[min(78%,26rem)]',
  mobile: 'w-[min(62%,16rem)]',
};

// Tablets and phones get a device frame; desktops a window edge.
const frames: Record<DeviceId, string> = {
  desktop: 'rounded-xl border',
  tablet: 'rounded-[1.75rem] border-[6px] border-fd-muted',
  mobile: 'rounded-[2rem] border-[6px] border-fd-muted',
};

/** One carousel per screen size, stacked: desktop, tablet, mobile. */
export function ScreenshotGallery({ items }: { items: Screenshot[] }) {
  return (
    <div className="mt-10 flex w-full flex-col gap-16 text-left">
      {devices.map((d) => (
        <Carousel key={d.id} device={d.id} items={items} />
      ))}
    </div>
  );
}

function Carousel({ device, items }: { device: DeviceId; items: Screenshot[] }) {
  const size = devices.find((d) => d.id === device)!;
  const Icon = icons[device];
  const track = useRef<HTMLDivElement>(null);
  const [index, setIndex] = useState(0);

  // The current slide is the one nearest the track's centre.
  useEffect(() => {
    const el = track.current;
    if (!el) return;
    const update = () => {
      const mid = el.scrollLeft + el.clientWidth / 2;
      let best = 0;
      let dist = Infinity;
      Array.from(el.children).forEach((c, i) => {
        const s = c as HTMLElement;
        const d = Math.abs(s.offsetLeft + s.offsetWidth / 2 - mid);
        if (d < dist) [best, dist] = [i, d];
      });
      setIndex(best);
    };
    el.addEventListener('scroll', update, { passive: true });
    return () => el.removeEventListener('scroll', update);
  }, []);

  function go(i: number) {
    const el = track.current;
    const target = el?.children[Math.max(0, Math.min(items.length - 1, i))] as HTMLElement | undefined;
    if (!el || !target) return;
    el.scrollTo({ left: target.offsetLeft - (el.clientWidth - target.offsetWidth) / 2, behavior: 'smooth' });
  }

  const current = items[index];

  return (
    <section aria-labelledby={`carousel-${device}`} aria-roledescription="carousel">
      <div className="flex items-end justify-between gap-4">
        <h2 id={`carousel-${device}`} className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Icon className="size-6 text-fd-primary" aria-hidden />
          {size.label}
        </h2>
        <div className="flex items-center gap-3">
          <span className="text-sm tabular-nums text-fd-muted-foreground" aria-live="polite">
            {current?.title} · {index + 1} / {items.length}
          </span>
          <NavButton label={`Previous ${size.label.toLowerCase()} screenshot`} onClick={() => go(index - 1)} disabled={index === 0}>
            <ChevronLeft className="size-5" aria-hidden />
          </NavButton>
          <NavButton
            label={`Next ${size.label.toLowerCase()} screenshot`}
            onClick={() => go(index + 1)}
            disabled={index === items.length - 1}
          >
            <ChevronRight className="size-5" aria-hidden />
          </NavButton>
        </div>
      </div>

      <div
        ref={track}
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'ArrowRight') (e.preventDefault(), go(index + 1));
          if (e.key === 'ArrowLeft') (e.preventDefault(), go(index - 1));
        }}
        className="mt-5 flex snap-x snap-mandatory gap-6 overflow-x-auto scroll-smooth pb-4 outline-none [scrollbar-width:thin] focus-visible:ring-2 focus-visible:ring-fd-ring"
      >
        {items.map((s, i) => {
          const src = screenshotSrc(s.slug, device);
          return (
            <figure
              key={s.slug}
              aria-roledescription="slide"
              aria-label={`${i + 1} of ${items.length}: ${s.title}`}
              className={`flex shrink-0 snap-center flex-col gap-3 ${slides[device]}`}
            >
              <a
                href={src}
                target="_blank"
                rel="noreferrer"
                className={`overflow-hidden border-fd-border bg-fd-card transition-colors hover:border-fd-primary ${frames[device]}`}
              >
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img
                  src={src}
                  alt={`${s.title} on a ${size.label.toLowerCase()} screen`}
                  width={size.width}
                  height={size.height}
                  loading={i < 2 ? 'eager' : 'lazy'}
                  className="h-auto w-full"
                />
              </a>
              <figcaption className="flex items-center justify-between gap-3">
                <span className="font-medium">{s.title}</span>
                <Link
                  href={s.docs}
                  className="inline-flex items-center gap-1 text-sm text-fd-primary underline-offset-4 hover:underline"
                >
                  Docs
                  <ArrowRight className="size-3.5" aria-hidden />
                </Link>
              </figcaption>
            </figure>
          );
        })}
      </div>
    </section>
  );
}

function NavButton({
  label,
  onClick,
  disabled,
  children,
}: {
  label: string;
  onClick: () => void;
  disabled: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
      className="inline-flex size-10 items-center justify-center rounded-full border border-fd-border bg-fd-card transition-colors hover:bg-fd-accent disabled:opacity-40"
    >
      {children}
    </button>
  );
}
