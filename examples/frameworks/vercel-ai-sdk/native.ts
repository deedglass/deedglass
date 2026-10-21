// The native route: @deedglass/sdk's AI SDK adapter. Needs AI_GATEWAY_API_KEY, DEEDGLASS_API_KEY and
// DEEDGLASS_SERVER.
import { generateText, stepCountIs } from 'ai';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/ai-sdk';

// The org's connector tools, named postgres__query and so on; Deedglass runs each one.
const tools = await deedglassTools(new Deedglass(), { include: ['postgres.*'] });
const { text } = await generateText({ model: 'anthropic/claude-sonnet-5.5', tools, stopWhen: stepCountIs(5), prompt: 'How many rows are in public.orders?' });
console.log(text);
