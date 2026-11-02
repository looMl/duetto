<script lang="ts">
  // Minimal stroke icon set (24×24, 1.75 stroke). Paths are original.
  type Name =
    | 'play' | 'pause' | 'volume' | 'mute' | 'cc' | 'expand' | 'shrink'
    | 'users' | 'link' | 'check' | 'x' | 'settings' | 'back10' | 'fwd10';
  let { name, size = 22 }: { name: Name; size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
  stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  {#if name === 'play'}
    <path d="M7 4.5v15l12.5-7.5z" fill="currentColor" stroke="none" />
  {:else if name === 'pause'}
    <rect x="6" y="4.5" width="4" height="15" rx="1" fill="currentColor" stroke="none" />
    <rect x="14" y="4.5" width="4" height="15" rx="1" fill="currentColor" stroke="none" />
  {:else if name === 'volume'}
    <path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" /><path d="M15.5 9a4 4 0 0 1 0 6" /><path d="M18 6.5a7.5 7.5 0 0 1 0 11" />
  {:else if name === 'mute'}
    <path d="M4 9.5h3.5L12 5.5v13l-4.5-4H4z" /><path d="m16 10 4 4m0-4-4 4" />
  {:else if name === 'cc'}
    <rect x="3" y="5.5" width="18" height="13" rx="2.5" /><path d="M10 10.5a2 2 0 1 0 0 3M16.5 10.5a2 2 0 1 0 0 3" />
  {:else if name === 'expand'}
    <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
  {:else if name === 'shrink'}
    <path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" />
  {:else if name === 'users'}
    <circle cx="9" cy="8.5" r="3.5" /><path d="M2.5 19.5a6.5 6.5 0 0 1 13 0" /><path d="M16 5.2a3.5 3.5 0 0 1 0 6.6M18.5 14a6.5 6.5 0 0 1 3 5.5" />
  {:else if name === 'link'}
    <path d="M10 14a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1" /><path d="M14 10a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1" />
  {:else if name === 'check'}
    <path d="m5 12.5 4.5 4.5L19 7.5" />
  {:else if name === 'x'}
    <path d="M6 6l12 12M18 6 6 18" />
  {:else if name === 'settings'}
    <path d="M4 7h10M18 7h2M4 17h4M12 17h8" /><circle cx="16" cy="7" r="2" /><circle cx="10" cy="17" r="2" />
  {:else if name === 'back10'}
    <path d="M4.5 12a7.5 7.5 0 1 0 2.2-5.3" /><path d="M4.5 4.5v3.5H8" />
  {:else if name === 'fwd10'}
    <path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3" /><path d="M19.5 4.5v3.5H16" />
  {/if}
</svg>
