// docs.deedglass.com (S33, ADR-064): Astro + Starlight, static output, no third-party requests.
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightOpenAPI from 'starlight-openapi';
import starlightLlmsTxt from 'starlight-llms-txt';
import { readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { sidebar } from './sidebar.mjs';
import { PAGE_MARK, POSTGRES_SET, description, inSmall, inlineSmallCode, llmsDetails, walkFiles } from './scripts/seo.mjs';

// The one description (docs/description.json; docs/description_test.go checks that this file reads it).
const desc = description();

// llms-small.txt gets the pages inSmall() picks (scripts/seo.mjs); the plugin takes the rest as page ids to
// exclude. sync-docs has written the imported and generated pages by the time this runs.
const contentDir = fileURLToPath(new URL('./src/content/docs/', import.meta.url));
const pageIds = walkFiles(contentDir)
  .filter((f) => /\.mdx?$/.test(f))
  .map((f) => relative(contentDir, f).replace(/\\/g, '/').replace(/\.mdx?$/, '').replace(/(^|\/)index$/, ''));
const notSmall = pageIds.filter((id) => !inSmall(id)).flatMap((id) => [id, `${id}/index`]);

const inkTheme = JSON.parse(readFileSync(new URL('./src/styles/deedglass-ink.json', import.meta.url), 'utf8'));

// Routes the validator can't see (the OpenAPI plugin's pages and src/pages): scripts/check-dist-links.mjs
// checks every link and anchor in dist/ instead, these included.
const customPages = ['/reference/api/', '/reference/api/**', '/_kit/'];

export default defineConfig({
  site: 'https://docs.deedglass.com',
  trailingSlash: 'always',
  image: { service: passthroughImageService() },
  // DeedCard bundles web/verify/src (S05's verifier) from outside docs-site/.
  // DOC-LCP: stylesheets and scripts under 4 KB go inline (fewer requests before first paint; postbuild
  // hashes every inline script into the CSP). Nothing else is ever inlined: a font or image as a data:
  // URL would break the CSP's font-src 'self'.
  vite: { server: { fs: { allow: ['..'] } }, build: { assetsInlineLimit: inlineSmallCode } },
  integrations: [
    starlight({
      title: 'Deedglass Docs',
      description: desc.tagline,
      favicon: '/favicon.svg',
      head: [
        { tag: 'link', attrs: { rel: 'icon', href: '/favicon.ico', sizes: 'any' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#07080C', media: '(prefers-color-scheme: dark)' } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#F7F7F4', media: '(prefers-color-scheme: light)' } },
      ],
      customCss: [
        '@fontsource-variable/inter/index.css',
        '@fontsource/geist-mono/400.css',
        '@fontsource/geist-mono/500.css',
        './src/styles/tokens.gen.css',
        './src/styles/deedglass.css',
      ],
      components: {
        SiteTitle: './src/components/overrides/SiteTitle.astro',
        Footer: './src/components/overrides/Footer.astro',
        Hero: './src/components/overrides/Hero.astro',
        SocialIcons: './src/components/overrides/SocialIcons.astro',
      },
      disable404Route: true,
      routeMiddleware: './src/routeData.ts',
      lastUpdated: false,
      credits: false,
      pagination: true,
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
      expressiveCode: {
        themes: [inkTheme],
        useStarlightDarkModeSwitch: false,
        useStarlightUiThemeColors: false,
        defaultProps: { wrap: true, preserveIndent: true },
        styleOverrides: {
          borderRadius: '10px',
          borderColor: '#1C1F2A',
          codeFontFamily: "'Geist Mono', ui-monospace, SFMono-Regular, Menlo, monospace",
          codeFontSize: '0.875rem',
          codeLineHeight: '1.5714',
          uiFontFamily: "'Inter Variable', ui-sans-serif, system-ui, sans-serif",
          frames: {
            editorBackground: '#0E1017',
            terminalBackground: '#0E1017',
            editorTabBarBackground: '#141722',
            terminalTitlebarBackground: '#141722',
            editorActiveTabBackground: '#0E1017',
            editorActiveTabIndicatorTopColor: '#6F8DFF',
            terminalTitlebarBorderBottomColor: '#1C1F2A',
            editorTabBarBorderBottomColor: '#1C1F2A',
            frameBoxShadowCssValue: 'none',
            inlineButtonBackground: '#1C1F2A',
            inlineButtonForeground: '#F5F6FA',
            inlineButtonBorder: '#2A2E3B',
            tooltipSuccessBackground: '#0F2A28',
            tooltipSuccessForeground: '#34E3C9',
          },
        },
      },
      sidebar,
      plugins: [
        starlightLinksValidator({ exclude: customPages, errorOnLocalLinks: true }),
        starlightOpenAPI([
          { base: 'reference/api', schema: './src/data/openapi.docs.json', sidebar: { label: 'Endpoints', collapsed: true } },
        ]),
        // DOC-01, DOC-02, DOC-06: postbuild.mjs drops the error pages, the API reference and the engineering
        // notes from llms-full.txt (the plugin's exclude covers llms-small.txt only), copies the Postgres
        // set to /llms-postgres.txt, and check-llms.mjs holds the budgets and the wording.
        starlightLlmsTxt({
          projectName: 'Deedglass',
          description: desc.summary,
          details: llmsDetails(desc.licence),
          exclude: ['_kit', ...notSmall],
          customSets: [POSTGRES_SET],
          pageSeparator: PAGE_MARK,
          promote: ['quickstart', 'concepts/what-matches-means', 'guides/check-what-an-agent-changed-in-postgres', 'concepts/how-it-works'],
        }),
      ],
    }),
  ],
});
