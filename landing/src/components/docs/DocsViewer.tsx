import { component$, useVisibleTask$ } from "@builder.io/qwik";
import type { Signal } from "@builder.io/qwik";
import { useLocation } from "@builder.io/qwik-city";
import { SIDEBAR } from "../../data/docs-content";
import { isDocsPath, renderDocPageHtml } from "../../data/docs-html";

interface DocsViewerProps {
  currentDoc: Signal<string | null>;
}

export const DocsViewer = component$<DocsViewerProps>(({ currentDoc }) => {
  const loc = useLocation();
  const docsRoute = isDocsPath(loc.url.pathname);

  // Register window.__go / go() so in-doc handlers work.
  // On /docs/[slug], navigate to the crawlable URL; on /?doc=, keep the SPA.
  // Content is static, trusted HTML from src/data/docs-content.ts — not user input.
  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(() => {
    const go = (id: string) => {
      if (isDocsPath(window.location.pathname)) {
        window.location.assign(`/docs/${id}`);
        return false;
      }
      currentDoc.value = id;
      window.history.pushState({}, "", `/?doc=${id}`);
      window.scrollTo(0, 0);
      return false;
    };
    (window as any).__go = go;
    (window as any).go = go;
  });

  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(({ track }) => {
    track(() => currentDoc.value);
    requestAnimationFrame(() => {
      document.querySelectorAll(".dcards .dcard").forEach((card, i) => {
        (card as HTMLElement).style.setProperty(
          "--stagger",
          `${(i % 8) * 70}ms`,
        );
        card.classList.add("ani");
      });
    });
  });

  const id = currentDoc.value ?? "docs-intro";
  const content = renderDocPageHtml(id);

  if (!content) {
    return (
      <div class="dl">
        <main class="dc">
          <p>Doc not found: {id}</p>
        </main>
      </div>
    );
  }

  return (
    <div class="dl">
      <aside class="ds" id="docSidebar">
        <button
          class="mob-sb-close"
          onClick$={() => {
            document.getElementById("docSidebar")?.classList.remove("mob-open");
          }}
        >
          ✕ Close menu
        </button>
        <nav id="docSidebarNav">
          {SIDEBAR.map((section) => (
            <div key={section.label} class="dss">
              <div class="dsl">{section.label}</div>
              {section.items.map(([slug, label]) => (
                <a
                  key={slug}
                  class={`dsa${slug === id ? " act" : ""}`}
                  href={`/docs/${slug}`}
                  onClick$={(e) => {
                    if (docsRoute) return;
                    e.preventDefault();
                    currentDoc.value = slug;
                    window.history.pushState({}, "", `/?doc=${slug}`);
                    window.scrollTo(0, 0);
                  }}
                >
                  {label}
                </a>
              ))}
            </div>
          ))}
        </nav>
      </aside>
      <main class="dc">
        <button
          class="mob-docs-toggle"
          onClick$={() => {
            document.getElementById("docSidebar")?.classList.toggle("mob-open");
          }}
        >
          ☰ Contents
        </button>
        <div id="docContent" dangerouslySetInnerHTML={content} />
      </main>
    </div>
  );
});
