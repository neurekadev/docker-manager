import { loader } from 'fumadocs-core/source';
import { defineDocs } from 'fumadocs-mdx/macro';
import { metaSchema, pageSchema } from 'fumadocs-core/source/schema';

const docs = defineDocs({
  dir: 'content/docs',
  docs: { schema: pageSchema },
  meta: { schema: metaSchema },
});

export const source = loader({
  // Docs pages live at the site's root (/quickstart/), next to the landing
  // page (/) and /screenshots/.
  baseUrl: '/',
  source: docs.toFumadocsSource(),
});
