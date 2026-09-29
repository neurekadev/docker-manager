'use client';
import { useState } from 'react';
import Link from 'next/link';
import { ArrowRight, Monitor, Smartphone, Tablet } from 'lucide-react';
import { devices, screenshotSrc, type DeviceId, type Screenshot } from '@/lib/screenshots';

const icons = { desktop: Monitor, tablet: Tablet, mobile: Smartphone } satisfies Record<DeviceId, unknown>;

// Wider screens get fewer columns, so every screenshot stays readable.
const grids: Record<DeviceId, string> = {
  desktop: 'grid-cols-1 lg:grid-cols-2',
  tablet: 'grid-cols-1 sm:grid-cols-2 lg:grid-cols-3',
  mobile: 'grid-cols-2 sm:grid-cols-3 lg:grid-cols-4',
};

export function ScreenshotGallery({ items }: { items: Screenshot[] }) {
  const [device, setDevice] = useState<DeviceId>('desktop');
  const size = devices.find((d) => d.id === device)!;

  return (
    <>
      <div
        role="radiogroup"
        aria-label="Screen size"
        className="mx-auto mt-8 inline-flex rounded-xl border border-fd-border bg-fd-card p-1"
      >
        {devices.map((d) => {
          const Icon = icons[d.id];
          const active = d.id === device;
          return (
            <button
              key={d.id}
              type="button"
              role="radio"
              aria-checked={active}
              onClick={() => setDevice(d.id)}
              className={`inline-flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium transition-colors ${
                active ? 'bg-fd-primary text-fd-primary-foreground' : 'text-fd-muted-foreground hover:bg-fd-accent'
              }`}
            >
              <Icon className="size-4" aria-hidden />
              {d.label}
            </button>
          );
        })}
      </div>

      <div className={`mt-10 grid w-full gap-8 text-left ${grids[device]}`}>
        {items.map((s) => {
          const src = screenshotSrc(s.slug, device);
          return (
            <figure key={s.slug} className="flex flex-col gap-3">
              <a
                href={src}
                target="_blank"
                rel="noreferrer"
                className="overflow-hidden rounded-xl border border-fd-border bg-fd-card transition-colors hover:border-fd-primary"
              >
                {/* eslint-disable-next-line @next/next/no-img-element */}
                <img
                  src={src}
                  alt={`${s.title} on a ${size.label.toLowerCase()} screen`}
                  width={size.width}
                  height={size.height}
                  loading="lazy"
                  className="h-auto w-full"
                />
              </a>
              <figcaption className="flex items-center justify-between gap-3">
                <span className="font-medium">{s.title}</span>
                <Link
                  href={s.docs}
                  className="inline-flex items-center gap-1 text-sm text-fd-primary underline-offset-4 hover:underline"
                >
                  Read the docs
                  <ArrowRight className="size-3.5" aria-hidden />
                </Link>
              </figcaption>
            </figure>
          );
        })}
      </div>
    </>
  );
}
