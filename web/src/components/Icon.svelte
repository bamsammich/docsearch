<script lang="ts" module>
  // One stroke set for the whole UI, drawn at 24 units.
  const paths = {
    search: '<circle cx="11" cy="11" r="6.5"/><path d="M16 16l4.5 4.5"/>',
    refresh: '<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3"/><path d="M19.5 4.5v4.2h-4.2"/>',
    trash: '<path d="M4.5 7h15"/><path d="M9.5 7V4.8h5V7"/><path d="M6.5 7l1 12.5h9l1-12.5"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    up: '<path d="M12 16V4.5"/><path d="M7 9.5l5-5 5 5"/><path d="M4.5 19.5h15"/>',
    x: '<path d="M6 6l12 12M18 6L6 18"/>',
    back: '<path d="M15 5.5L8.5 12l6.5 6.5"/>',
    check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
    warn: '<path d="M12 4l9 16H3z"/><path d="M12 10v4.5"/><path d="M12 17.2v.3"/>',
    stop: '<circle cx="12" cy="12" r="8.5"/><path d="M8.5 12h7"/>',
    play: '<circle cx="12" cy="12" r="8.5"/><path d="M10.5 9l4 3-4 3z"/>',
    chevron: '<path d="M9.5 6l6 6-6 6"/>',
    eye: '<path d="M2.5 12s3.5-6.5 9.5-6.5 9.5 6.5 9.5 6.5-3.5 6.5-9.5 6.5S2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
    "eye-off": '<path d="M4 4l16 16"/><path d="M10.6 5.6A10 10 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-2.6 3.4M6.6 6.6C3.9 8.4 2.5 12 2.5 12S6 18.5 12 18.5a9.6 9.6 0 0 0 4.2-.9"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/>',
    alert: '<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5"/><path d="M12 16.3v.2"/>',
    library: '<path d="M4.5 5.5h15M4.5 12h15M4.5 18.5h9"/>',
    inbox: '<path d="M3.5 13.5l2.6-7.2A1.5 1.5 0 0 1 7.5 5.3h9a1.5 1.5 0 0 1 1.4 1l2.6 7.2v4.7a1.5 1.5 0 0 1-1.5 1.5H5a1.5 1.5 0 0 1-1.5-1.5z"/><path d="M3.5 13.5h4.7l1.3 2.5h5l1.3-2.5h4.7"/>',
    key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l8.5-8.5M16.5 6.5l2.5 2.5M14 9l2 2"/>',
    scan: '<path d="M4 8V5.5A1.5 1.5 0 0 1 5.5 4H8M16 4h2.5A1.5 1.5 0 0 1 20 5.5V8M20 16v2.5a1.5 1.5 0 0 1-1.5 1.5H16M8 20H5.5A1.5 1.5 0 0 1 4 18.5V16"/><path d="M8 9.5h8M8 12h8M8 14.5h5"/>',
    read: '<path d="M12 6.5C10 5 7.2 4.5 3.5 5v13c3.7-.5 6.5 0 8.5 1.5 2-1.5 4.8-2 8.5-1.5V5c-3.7-.5-6.5 0-8.5 1.5z"/><path d="M12 6.5v13"/>',
    external: '<path d="M13.5 4.5h6v6"/><path d="M19.5 4.5L11 13"/><path d="M17.5 14v4a1.5 1.5 0 0 1-1.5 1.5H6A1.5 1.5 0 0 1 4.5 18V8A1.5 1.5 0 0 1 6 6.5h4"/>',
    pencil: '<path d="M14.5 5.5l4 4L8 20H4v-4z"/><path d="M12.5 7.5l4 4"/>',
    filter: '<path d="M4 6.5h16M7 12h10M10 17.5h4"/>',
    expand: '<path d="M14 4.5h5.5V10"/><path d="M19.5 4.5L13.5 10.5"/><path d="M10 19.5H4.5V14"/><path d="M4.5 19.5l6-6"/>',
    collapse: '<path d="M19.5 10H14V4.5"/><path d="M14 10l5.5-5.5"/><path d="M4.5 14H10v5.5"/><path d="M10 14l-5.5 5.5"/>',
    selector: '<path d="M8 9.5l4-4 4 4"/><path d="M8 14.5l4 4 4-4"/>',
    members: '<circle cx="9" cy="8.5" r="3.5"/><path d="M2.5 19.5c.8-3.4 3.4-5.5 6.5-5.5s5.7 2.1 6.5 5.5"/><path d="M15.5 5.2a3.5 3.5 0 0 1 0 6.6M18 14.4c1.8.9 3 2.7 3.5 5.1"/>',
    person: '<circle cx="12" cy="8" r="3.5"/><path d="M5 20c.9-3.6 3.7-6 7-6s6.1 2.4 7 6"/>',
    lock: '<rect x="5" y="10.5" width="14" height="9.5" rx="2"/><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"/>',
    shield: '<path d="M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z"/>',
    download: '<path d="M12 4.5v10"/><path d="M7.5 10.5l4.5 4.5 4.5-4.5"/><path d="M5 19.5h14"/>',
    copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="1.5"/><path d="M15.5 8.5V6A1.5 1.5 0 0 0 14 4.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5"/>',
  } as const;

  export type IconName = keyof typeof paths;
</script>

<script lang="ts">
  let { name }: { name: IconName } = $props();
</script>

<svg class="i" viewBox="0 0 24 24" aria-hidden="true">{@html paths[name]}</svg>
