'use client';
import SearchDialog from '@/components/search';
import { RootProvider } from 'fumadocs-ui/provider/next';
import { type ReactNode } from 'react';

// Dark only: the theme is forced, system and stored preferences are ignored
// and the light/dark hotkey is off.
export function Provider({ children }: { children: ReactNode }) {
  return (
    <RootProvider
      search={{ SearchDialog }}
      theme={{ forcedTheme: 'dark', defaultTheme: 'dark', enableSystem: false, hotKey: false }}
    >
      {children}
    </RootProvider>
  );
}
