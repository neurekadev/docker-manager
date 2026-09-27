import type { Metadata } from 'next';
import { Provider } from '@/components/provider';
import { appName } from '@/lib/shared';
import './global.css';

export const metadata: Metadata = {
  title: { template: `%s | ${appName}`, default: appName },
  description: 'Install and run Docker Manager, the self-hosted web UI for your Docker hosts.',
};

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <html lang="en" className="dark" style={{ colorScheme: 'dark' }} suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <Provider>{children}</Provider>
      </body>
    </html>
  );
}
