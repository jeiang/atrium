import type { Model, ModelConfiguration } from '../../contract';

export interface Scope {
  model: string;
  effort: string;
  context: number;
}

export function contextChoices(options: ModelConfiguration | undefined, model: Model | undefined): number[] {
  return (options?.context_limits ?? []).filter((limit) => !model || limit <= model.context_window);
}

export function snapContext(choices: readonly number[], context: number): number {
  return choices.findLast((limit) => limit <= context) ?? choices[0] ?? context;
}

export function contextLabel(limit: number): string {
  return limit >= 1_000_000 ? `${limit / 1_000_000}M` : `${Math.round(limit / 1000)}k`;
}

function thinkingFor(
  options: ModelConfiguration | undefined,
  model: Model | undefined,
  thinking: string,
  effort: string,
): string {
  if (!model || model.thinking.length === 0) return thinking;
  const found = model.thinking.find((entry) => entry.type === thinking);
  const efforts = options?.efforts ?? [];
  if (found && (!found.max_effort || efforts.indexOf(effort) <= efforts.indexOf(found.max_effort))) return thinking;
  return model.thinking[0]?.type ?? thinking;
}

export function startOptions(options: ModelConfiguration | undefined, scope: Scope) {
  const model = options?.models.find((entry) => entry.id === scope.model);
  const thinking = options ? thinkingFor(options, model, options.defaults.thinking, scope.effort) : '';
  return {
    model: scope.model || undefined,
    effort: scope.effort || undefined,
    context_limit: scope.context || undefined,
    thinking: thinking || undefined,
  };
}

export function effortFixed(model: Model | undefined, thinking: string): boolean {
  return Boolean(model?.thinking.find((entry) => entry.type === thinking)?.max_effort);
}
