import { component$ } from '@builder.io/qwik';
import type { DocumentHead } from '@builder.io/qwik-city';
import { SEO_DOCS } from '../../data/docs';

export default component$(() => {
  return (
    <main class="seo-docs">
      <div class="seo-wrap">
        <p class="seo-kicker">Kaptanto Documentation</p>
        <h1>Documentation index</h1>
        <p>Browse all documentation pages in crawlable URLs.</p>
        <ul>
          {SEO_DOCS.map((d) => (
            <li key={d.slug}>
              <a href={`/docs/${d.slug}`}>{d.title}</a>
              <span>{d.description}</span>
            </li>
          ))}
        </ul>
      </div>
    </main>
  );
});

export const head: DocumentHead = {
  title: 'Kaptanto Docs Index',
  meta: [
    {
      name: 'description',
      content: 'Crawlable documentation index for Kaptanto CDC guides and references.',
    },
  ],
};
