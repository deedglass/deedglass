// Per-page head entries:
//   - Open Graph images per section (public/og/*.png, made by scripts/og.mjs; brand §13.7).
//   - DOC-05 (scripts/seo.mjs): noindex on the thin per-code pages, a <title> of at most 60
//     characters, and TechArticle JSON-LD (WebSite on the home page) with the source's last commit
//     date (src/data/.page-dates.json, written by sync-docs). seo-aeo-v1: step-by-step pages add a HowTo in the
//     same block (an @graph), its steps the page's own headings.
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { isThin, jsonLd, ldScript, pageTitle } from '../scripts/seo.mjs';
import dates from './data/.page-dates.json';
import desc from '../../docs/description.json';

const sections = new Set(['get-started', 'concepts', 'guides', 'reference', 'errors', 'security']);
const pageDates: Record<string, string> = dates;

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

  const path = context.url.pathname.endsWith('/') ? context.url.pathname : context.url.pathname + '/';
  const data = route.entry.data;
  const title = route.head.find((h) => h.tag === 'title');
  if (title) title.content = pageTitle(data.title, path);
  // freshness-1019: the OpenAPI overview has no description of its own and took the home page's.
  if (path === '/reference/api/') {
    const d = route.head.find((h) => h.tag === 'meta' && h.attrs?.name === 'description');
    if (title) title.content = pageTitle('HTTP API reference');
    if (d?.attrs) d.attrs.content = 'Every Deedglass HTTP API endpoint, generated from the OpenAPI spec: requests, responses, errors and examples for API version 2026-10-01.';
  }
  if (isThin(path)) {
    route.head.push({ tag: 'meta', attrs: { name: 'robots', content: 'noindex, follow' } });
    return;
  }
  const ld = jsonLd({ path, title: data.hero?.title || data.title, description: data.description ?? '', dateModified: pageDates[path], tagline: desc.tagline, headings: route.headings });
  route.head.push({ tag: 'script', attrs: { type: 'application/ld+json' }, content: ldScript(ld) });
});
