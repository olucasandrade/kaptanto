import { component$ } from '@builder.io/qwik';
import type { DocumentHead } from '@builder.io/qwik-city';
import { useLocation } from '@builder.io/qwik-city';
import { SEO_DOCS, SEO_DOCS_MAP } from '../../../data/docs';

export default component$(() => {
  const loc = useLocation();
  const slug = loc.params.slug;
  const doc = SEO_DOCS_MAP.get(slug);

  if (!doc) {
    return (
      <main class="seo-docs">
        <div class="seo-wrap">
          <h1>Documentation page not found</h1>
          <p>Return to the <a href="/docs">documentation index</a>.</p>
        </div>
      </main>
    );
  }

  const idx = SEO_DOCS.findIndex((d) => d.slug === slug);
  const next = SEO_DOCS[(idx + 1) % SEO_DOCS.length];
  const next2 = SEO_DOCS[(idx + 2) % SEO_DOCS.length];

  return (
    <main class="seo-docs">
      <div class="seo-wrap">
        <p class="seo-kicker">Kaptanto Docs</p>
        <h1>{doc.title}</h1>
        <p>{doc.description}</p>
        <nav class="seo-nav" aria-label="Next documentation pages">
          <a href={`/docs/${next.slug}`}>Next: {next.title}</a>
          <a href={`/docs/${next2.slug}`}>Then: {next2.title}</a>
          <a href="/?doc=docs-intro">Open interactive docs view</a>
        </nav>
      </div>
    </main>
  );
});

export const head: DocumentHead = ({ params }) => {
  const doc = SEO_DOCS_MAP.get(params.slug);
  if (!doc) {
    return {
      title: 'Kaptanto Docs | Not Found',
      meta: [{ name: 'robots', content: 'noindex,follow' }],
    };
  }

  const canonical = `https://kaptanto.dev/docs/${doc.slug}`;
  return {
    title: `${doc.title} | Kaptanto Docs`,
    meta: [
      { name: 'description', content: doc.description },
      { property: 'og:type', content: 'article' },
      { property: 'og:title', content: `${doc.title} | Kaptanto Docs` },
      { property: 'og:description', content: doc.description },
      { property: 'og:url', content: canonical },
      { property: 'og:image', content: 'https://kaptanto.dev/logo.png' },
      { name: 'twitter:card', content: 'summary' },
    ],
  };
};
