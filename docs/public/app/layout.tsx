import type { Metadata, Viewport } from 'next';
import { Provider } from '@/components/provider';
import { appName, brandColor } from '@/lib/shared';
import { metadataBase, shareMetadata } from '@/lib/metadata';
import './global.css';

// Link previews: the share tags come from lib/metadata.ts; theme-color
// colours the preview's strip (Discord) with the logo's blue.
export const metadata: Metadata = {
  metadataBase,
  title: { template: `%s | ${appName}`, default: appName },
  ...shareMetadata(),
};

export const viewport: Viewport = { themeColor: brandColor };

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <html lang="en" className="dark" style={{ colorScheme: 'dark' }} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
