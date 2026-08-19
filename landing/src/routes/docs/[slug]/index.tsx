import { component$, useSignal, useTask$ } from "@builder.io/qwik";
import type {
  DocumentHead,
  StaticGenerateHandler,
} from "@builder.io/qwik-city";
import { useLocation } from "@builder.io/qwik-city";
import { DocsViewer } from "../../../components/docs/DocsViewer";
import { Nav } from "../../../components/nav/Nav";
import { SEO_DOCS, SEO_DOCS_MAP } from "../../../data/docs";
import { DOCS_CONTENT } from "../../../data/docs-content";

const siteStyleLinks = [
  { rel: "preconnect", href: "https://fonts.googleapis.com" },
  {
    rel: "preconnect",
    href: "https://fonts.gstatic.com",
    crossorigin: "anonymous" as const,
  },
  {
    rel: "stylesheet",
    href: "https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600;700&family=IBM+Plex+Sans:wght@300;400;500;600;700&display=swap",
  },
  { rel: "stylesheet", href: "/legacy.css" },
  { rel: "stylesheet", href: "/ui-fixes.css" },
];

export default component$(() => {
  const loc = useLocation();
  const currentDoc = useSignal<string | null>(loc.params.slug);

  useTask$(({ track }) => {
    currentDoc.value = track(() => loc.params.slug);
  });

  const slug = loc.params.slug;
  if (!SEO_DOCS_MAP.has(slug) || !DOCS_CONTENT[slug]) {
    return (
      <main class="seo-docs">
        <div class="seo-wrap">
          <h1>Documentation page not found</h1>
          <p>
            Return to the <a href="/docs">documentation index</a>.
          </p>
        </div>
      </main>
    );
  }

  return (
    <>
      <Nav currentDoc={currentDoc} />
      <DocsViewer currentDoc={currentDoc} />
    </>
  );
});

export const onStaticGenerate: StaticGenerateHandler = async () => {
  return {
    params: SEO_DOCS.map((d) => ({ slug: d.slug })),
  };
};

export const head: DocumentHead = ({ params }) => {
  const doc = SEO_DOCS_MAP.get(params.slug);
  if (!doc) {
    return {
      title: "Kaptanto Docs | Not Found",
      meta: [{ name: "robots", content: "noindex,follow" }],
    };
  }

  const canonical = `https://kaptan.to/docs/${doc.slug}`;
  return {
    title: `${doc.title} | Kaptanto Docs`,
    meta: [
      { name: "description", content: doc.description },
      { property: "og:type", content: "article" },
      { property: "og:title", content: `${doc.title} | Kaptanto Docs` },
      { property: "og:description", content: doc.description },
      { property: "og:url", content: canonical },
      { property: "og:image", content: "https://kaptan.to/og-image.png" },
      { name: "twitter:card", content: "summary" },
    ],
    links: siteStyleLinks,
  };
};
