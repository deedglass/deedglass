// The lifecycle states' chip styles (shared by StateChip.astro and chips.ts).
export const STATE_STYLES: Record<string, { cls: string; icon: string }> = {
  received: { cls: 'chip-neutral', icon: 'arrow-right' },
  authenticated: { cls: 'chip-neutral', icon: 'arrow-right' },
  evaluated: { cls: 'chip-neutral', icon: 'arrow-right' },
  denied: { cls: 'chip-denied', icon: 'ban' },
  pending_approval: { cls: 'chip-pending', icon: 'contrast' },
  executing: { cls: 'chip-neutral', icon: 'contrast' },
  executed: { cls: 'chip-neutral', icon: 'check' },
  failed: { cls: 'chip-neutral', icon: 'x' },
  witnessing: { cls: 'chip-pending', icon: 'witness' },
  verified: { cls: 'chip-verified', icon: 'check' },
  mismatch: { cls: 'chip-mismatch', icon: 'x' },
  unverifiable: { cls: 'chip-unverifiable', icon: 'circle-dashed' },
  sealed: { cls: 'chip-neutral', icon: 'deed' },
  expired: { cls: 'chip-outline', icon: 'clock' },
  cancelled: { cls: 'chip-outline', icon: 'x' },
};
