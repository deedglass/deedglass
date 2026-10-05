// docs.deedglass.com (S33, ADR-064): Astro + Starlight, static output, no third-party requests.
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import starlightOpenAPI from 'starlight-openapi';
import starlightLlmsTxt from 'starlight-llms-txt';
import { readFileSync } from 'node:fs';
import { sidebar } from './sidebar.mjs';

const inkTheme = JSON.parse(readFileSync(new URL('./src/styles/deedglass-ink.json', import.meta.url), 'utf8'));

// Routes the validator can't see (the OpenAPI plugin's pages and src/pages): scripts/check-dist-links.mjs
// checks every link and anchor in dist/ instead, these included.
const customPages = ['/reference/api/', '/reference/api/**', '/_kit/'];

export default defineConfig({
  site: 'https://docs.deedglass.com',
  trailingSlash: 'always',
  image: { service: passthroughImageService() },
  // DeedCard bundles web/verify/src (S05's verifier) from outside docs-site/.
  vite: { server: { fs: { allow: ['..'] } }, build: { assetsInlineLimit: 0 } },
  integrations: [
    starlight({
      title: 'Deedglass Docs',
      description:
        'Deedglass sits between your AI agents and the tools they use. It decides what each agent may do, runs it under control, checks the result against the system of record, and seals a deed.',
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
        starlightLlmsTxt({
          projectName: 'Deedglass',
          description:
            'Deedglass is the execution and accountability layer for AI agents. It sits between your agents and the tools they use: it decides what each agent may do, runs the action under control, checks the result against the system of record (L0 Reported, L1 Confirmed, L2 Witnessed by Postgres), and seals a signed deed anyone can verify offline. It is self-hosted, runs anywhere Postgres runs, and works with Claude, OpenAI, Gemini, Cursor and any MCP client.',
          details:
            '- Start with the Quickstart, then "What VERIFIED means" for the exact meaning of a verdict.\n- Every API error has its own page under /errors/<code>/.\n- Every feature page ends with "What it doesn\'t do".',
          exclude: ['_kit', 'reference/engineering/**'],
          promote: ['quickstart', 'concepts/what-verified-means', 'concepts/how-it-works'],
        }),
      ],
    }),
  ],
});
