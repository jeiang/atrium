import type { IconName } from '../shared/ui/Icon.svelte';

export interface AppDefinition {
  id: string;
  title: string;
  module: string;
  icon: IconName;
  legacy: string;
}
