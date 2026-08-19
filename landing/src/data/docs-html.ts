import { DOCS_CONTENT, SIDEBAR, DOC_FLOW } from "./docs-content";

/** True for the crawlable docs index and /docs/[slug] routes. */
export function isDocsPath(pathname: string): boolean {
  return pathname === "/docs" || pathname.startsWith("/docs/");
}

export function docHref(id: string): string {
  return `/docs/${id}`;
}

export function docLabel(id: string): string {
  for (const section of SIDEBAR) {
    for (const [slug, label] of section.items) {
      if (slug === id) return label;
    }
  }
  return DOCS_CONTENT[id]?.title ?? id;
}

/**
 * Rewrite SPA-only go() handlers in trusted DOCS_CONTENT HTML into crawlable
 * /docs/[slug] links. window.__go still intercepts clicks on the interactive
 * /?doc= view; without JS the hrefs work on the slug routes.
 */
export function rewriteDocLinks(html: string): string {
  return html
    .replace(
      /<div class="dcard" onclick="go\('([^']+)'\)">([\s\S]*?)<\/div>/g,
      (_match, slug: string, inner: string) =>
        `<a class="dcard" href="${docHref(slug)}" onclick="return window.__go && window.__go('${slug}')">${inner}</a>`,
    )
    .replace(
      /<a onclick="go\('([^']+)'\)">/g,
      (_match, slug: string) =>
        `<a href="${docHref(slug)}" onclick="return window.__go && window.__go('${slug}')">`,
    );
}

export function buildNextSteps(id: string): string {
  const i = DOC_FLOW.indexOf(id);
  if (i === -1) return "";
  const next1 = DOC_FLOW[(i + 1) % DOC_FLOW.length];
  const next2 = DOC_FLOW[(i + 2) % DOC_FLOW.length];
  return `<h2 class="dh2">Next steps</h2><div class="dcards">
<a class="dcard" href="${docHref(next1)}" onclick="return window.__go && window.__go('${next1}')"><h4>${docLabel(next1)}</h4><p>Next page.</p></a>
<a class="dcard" href="${docHref(next2)}" onclick="return window.__go && window.__go('${next2}')"><h4>${docLabel(next2)}</h4><p>Then read this.</p></a>
</div>`;
}

/** Same HTML DocsViewer SSR/SSG-emits so /docs/[slug] cannot drift from /?doc=. */
export function renderDocPageHtml(id: string): string | null {
  const doc = DOCS_CONTENT[id];
  if (!doc) return null;
  return `<div class="dhead"><img src="/logo.png" alt="Kaptanto logo" class="dlg"><h1>${doc.title}</h1></div><p class="dsub">${doc.sub}</p>${rewriteDocLinks(doc.body)}${buildNextSteps(id)}`;
}
