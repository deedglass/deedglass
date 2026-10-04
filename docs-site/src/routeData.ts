// Per-section Open Graph images (public/og/*.png, made by scripts/og.mjs; brand §13.7).
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';

const sections = new Set(['get-started', 'concepts', 'guides', 'reference', 'errors', 'security']);

export const onRequest = defineRouteMiddleware((context) => {
  const route = context.locals.starlightRoute;
  const first = route.id.split('/')[0];
  const img = sections.has(first) ? first : first === 'quickstart' ? 'get-started' : first === 'policies' ? 'reference' : 'default';
  const url = new URL(`/og/${img}.png`, context.site ?? 'https://docs.deedglass.com').href;
  route.head.push(
    { tag: 'meta', attrs: { property: 'og:image', content: url } },
    { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
    { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
    { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
    { tag: 'meta', attrs: { name: 'twitter:image', content: url } },
  );
});
