/**
 * Deedglass Adapter for Melaya AI Agent Platform (melaya.org)
 * Provides pre-call policy checks, credential isolation, and WAL audit deeds.
 */

import { Deedglass } from '@deedglass/sdk';
import { governCall, deedglassName, type GovernOptions } from '@deedglass/sdk/adapters';

export interface MelayaToolDefinition {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
  execute: (args: Record<string, unknown>, context?: unknown) => Promise<unknown>;
}

/**
 * Wraps Melaya agent tools with Deedglass governance.
 * Automatically checks policies before execution and seals signed deeds afterwards.
 */
export function governMelayaTools<T extends Record<string, MelayaToolDefinition>>(
  tools: T,
  options: GovernOptions
): T {
  const governed: Record<string, MelayaToolDefinition> = {};

  for (const [key, tool] of Object.entries(tools)) {
    const originalExecute = tool.execute;
    const dgToolName = deedglassName(options, tool.name || key);

    governed[key] = {
      ...tool,
      execute: async (args: Record<string, unknown>, context?: unknown) => {
        return governCall(
          options,
          dgToolName,
          args,
          async (signal) => {
            return await originalExecute.call(tool, args, context);
          }
        );
      },
    };
  }

  return governed as T;
}

export default governMelayaTools;

