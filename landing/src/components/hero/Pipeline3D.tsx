import { component$, useSignal, useVisibleTask$ } from "@builder.io/qwik";

/**
 * Decorative CSS-3D isometric CDC pipeline for the unused hero rail.
 * Hidden below 960px and when the user prefers reduced motion (static planes only).
 */
export const Pipeline3D = component$(() => {
  const show = useSignal(false);

  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(({ cleanup }) => {
    const mq = window.matchMedia("(min-width: 960px)");
    const sync = () => {
      show.value = mq.matches;
    };
    sync();
    mq.addEventListener("change", sync);
    cleanup(() => mq.removeEventListener("change", sync));
  });

  if (!show.value) return null;

  return (
    <div class="p3d" aria-hidden="true">
      <div class="p3d-stage">
        <div class="p3d-plane p3d-src">
          <span>source</span>
          <em>WAL / Change Streams</em>
        </div>
        <div class="p3d-plane p3d-log">
          <span>event log</span>
          <em>64 partitions · TTL</em>
        </div>
        <div class="p3d-plane p3d-sink">
          <span>sinks</span>
          <em>stdout · SSE · brokers</em>
        </div>
        <span class="p3d-dot p3d-dot-i" />
        <span class="p3d-dot p3d-dot-u" />
        <span class="p3d-dot p3d-dot-d" />
      </div>
    </div>
  );
});
