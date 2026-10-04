import { defineCollection, z } from 'astro:content';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

// S33 §2: every page carries lastVerified; drafts name the session that turns them on (owner);
// pages with third-party facts list their CLAIMS_REGISTER rows (scripts/check-claims.mjs).
export const collections = {
  docs: defineCollection({
    loader: docsLoader(),
    schema: docsSchema({
      extend: z.object({
        lastVerified: z.string().regex(/^\d{4}-\d{2}-\d{2}$/).optional(),
        owner: z.enum(['S47', 'S48']).optional(),
        claims: z.array(z.string().regex(/^C-\d{3}$/)).default([]),
        imported: z.string().optional(),
        // test-snippets.mjs: 'init' starts from an empty directory and runs deedglass init for real
        testEnv: z.enum(['demo', 'init']).optional(),
      }),
    }),
  }),
};
