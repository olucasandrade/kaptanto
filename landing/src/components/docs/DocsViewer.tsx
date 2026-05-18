import { component$, useVisibleTask$ } from '@builder.io/qwik';
import type { Signal } from '@builder.io/qwik';
import { DOCS_CONTENT, SIDEBAR, DOC_FLOW } from '../../data/docs-content';

interface DocsViewerProps {
  currentDoc: Signal<string | null>;
}

function docLabel(id: string): string {
  for (const section of SIDEBAR) {
    for (const [slug, label] of section.items) {
      if (slug === id) return label;
    }
  }
  return DOCS_CONTENT[id]?.title ?? id;
}

function buildNextSteps(id: string): string {
  const i = DOC_FLOW.indexOf(id);
  if (i === -1) return '';
  const next1 = DOC_FLOW[(i + 1) % DOC_FLOW.length];
  const next2 = DOC_FLOW[(i + 2) % DOC_FLOW.length];
  return `<h2 class="dh2">Next steps</h2><div class="dcards">
<a class="dcard" href="/docs/${next1}" onclick="return window.__go('${next1}')"><h4>${docLabel(next1)}</h4><p>Next page.</p></a>
<a class="dcard" href="/docs/${next2}" onclick="return window.__go('${next2}')"><h4>${docLabel(next2)}</h4><p>Then read this.</p></a>
</div>`;
}

export const DocsViewer = component$<DocsViewerProps>(({ currentDoc }) => {
  // Register window.__go so onclick="go(...)" handlers in the docs HTML work.
  // Content is static, trusted HTML from src/data/docs-content.ts — not user input.
  useVisibleTask$(() => {
    const go = (id: string) => {
      currentDoc.value = id;
      window.history.pushState({}, '', `/?doc=${id}`);
      window.scrollTo(0, 0);
      return false;
    };
    (window as any).__go = go;
    (window as any).go = go;
  });

  useVisibleTask$(({ track }) => {
    track(() => currentDoc.value);
    requestAnimationFrame(() => {
      document.querySelectorAll('.dcards .dcard').forEach((card, i) => {
        (card as HTMLElement).style.setProperty('--stagger', `${(i % 8) * 70}ms`);
        card.classList.add('ani');
      });
    });
  });

  const id = currentDoc.value ?? 'docs-intro';
  const doc = DOCS_CONTENT[id];

  if (!doc) {
    return (
      <div class="dl">
        <main class="dc">
          <p>Doc not found: {id}</p>
        </main>
      </div>
    );
  }

  // Trusted static content from docs-content.ts — safe to use dangerouslySetInnerHTML.
  const content = `<div class="dhead"><img src="/logo.png" alt="Kaptanto logo" class="dlg"><h1>${doc.title}</h1></div><p class="dsub">${doc.sub}</p>${doc.body}${buildNextSteps(id)}`;

  return (
    <div class="dl">
      <aside class="ds" id="docSidebar">
        <button
          class="mob-sb-close"
          onClick$={() => {
            document.getElementById('docSidebar')?.classList.remove('mob-open');
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
                  class={`dsa${slug === id ? ' act' : ''}`}
                  href={`/docs/${slug}`}
                  onClick$={(e) => {
                    e.preventDefault();
                    currentDoc.value = slug;
                    window.history.pushState({}, '', `/?doc=${slug}`);
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
            document.getElementById('docSidebar')?.classList.toggle('mob-open');
          }}
        >
          ☰ Contents
        </button>
        <div id="docContent" dangerouslySetInnerHTML={content} />
      </main>
    </div>
  );
});
