import { component$, useSignal, useVisibleTask$ } from '@builder.io/qwik';
import type { DocumentHead } from '@builder.io/qwik-city';
import { Nav } from '../components/nav/Nav';
import { LandingPage } from '../components/landing/LandingPage';
import { DocsViewer } from '../components/docs/DocsViewer';

const jsonLd = {
  '@context': 'https://schema.org',
  '@graph': [
    {
      '@type': 'WebSite',
      name: 'Kaptanto',
      url: 'https://kaptanto.dev/',
      inLanguage: 'en',
    },
    {
      '@type': 'Organization',
      name: 'Kaptanto',
      url: 'https://kaptanto.dev',
      logo: 'https://kaptanto.dev/logo.png',
      sameAs: ['https://github.com/olucasandrade/kaptanto'],
    },
    {
      '@type': 'SoftwareApplication',
      name: 'Kaptanto',
      applicationCategory: 'DeveloperApplication',
      operatingSystem: 'Linux, macOS, Windows',
      description:
        'Universal change data capture for Postgres and MongoDB with real-time event streaming.',
      license: 'https://opensource.org/licenses/MIT',
      downloadUrl: 'https://get.kaptan.to',
      softwareVersion: '0.2.0',
    },
    {
      '@type': 'FAQPage',
      mainEntity: [
        {
          '@type': 'Question',
          name: 'Does Kaptanto require Kafka?',
          acceptedAnswer: {
            '@type': 'Answer',
            text: 'No. Kaptanto can run as a standalone binary without Kafka.',
          },
        },
        {
          '@type': 'Question',
          name: 'Which databases are supported?',
          acceptedAnswer: {
            '@type': 'Answer',
            text: 'Kaptanto supports Postgres and MongoDB.',
          },
        },
      ],
    },
  ],
};

// currentDoc: null = landing page, string = docs viewer
export default component$(() => {
  const currentDoc = useSignal<string | null>(null);

  // Read initial route from URL and keep in sync with browser history.
  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(() => {
    const readUrl = () => {
      const path = window.location.pathname;
      const qs = new URLSearchParams(window.location.search);
      if (path.startsWith('/docs/')) {
        const slug = path.split('/docs/')[1].replace(/\/$/, '');
        currentDoc.value = slug || null;
      } else {
        currentDoc.value = qs.get('doc');
      }
    };

    readUrl();
    window.addEventListener('popstate', readUrl);
    return () => window.removeEventListener('popstate', readUrl);
  });

  // Scroll reveal: observe all .sr elements that haven't been revealed yet.
  // eslint-disable-next-line qwik/no-use-visible-task
  useVisibleTask$(({ track }) => {
    track(() => currentDoc.value);
    const obs = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            entry.target.classList.add('v');
            obs.unobserve(entry.target);
          }
        });
      },
      { threshold: 0.06 },
    );
    document.querySelectorAll('.sr:not(.v)').forEach((el) => obs.observe(el));
    return () => obs.disconnect();
  });

  return (
    <>
      <Nav currentDoc={currentDoc} />
      {currentDoc.value ? (
        <DocsViewer currentDoc={currentDoc} />
      ) : (
        <LandingPage currentDoc={currentDoc} />
      )}
    </>
  );
});

export const head: DocumentHead = {
  title: 'Kaptanto — Universal Database CDC',
  meta: [
    { name: 'author', content: 'Kaptanto' },
    { name: 'application-name', content: 'Kaptanto' },
    { name: 'theme-color', content: '#050505' },
    { name: 'format-detection', content: 'telephone=no' },
    {
      name: 'description',
      content:
        'Open-source universal CDC for Postgres and MongoDB. Stream real-time database changes via stdout, SSE, gRPC, or push directly to NATS, SQS, Kafka, Pub/Sub, and RabbitMQ — one lightweight binary.',
    },
    {
      name: 'robots',
      content: 'index,follow,max-image-preview:large,max-snippet:-1',
    },
    {
      name: 'keywords',
      content:
        'change data capture, cdc postgres, cdc mongodb, realtime database events, open source cdc, wal logical replication, kafka cdc, sqs cdc, nats cdc, pubsub cdc, rabbitmq cdc, queue sink',
    },
    { property: 'og:type', content: 'website' },
    { property: 'og:locale', content: 'en_US' },
    { property: 'og:site_name', content: 'Kaptanto' },
    { property: 'og:title', content: 'Kaptanto — Universal Database CDC' },
    {
      property: 'og:description',
      content:
        'Stream every insert, update, and delete from Postgres and MongoDB with one binary.',
    },
    { property: 'og:url', content: 'https://kaptanto.dev/' },
    { property: 'og:image', content: 'https://kaptanto.dev/logo.png' },
    { property: 'og:image:alt', content: 'Kaptanto logo' },
    { property: 'og:image:type', content: 'image/png' },
    { name: 'twitter:card', content: 'summary_large_image' },
    { name: 'twitter:site', content: '@kaptanto' },
    { name: 'twitter:title', content: 'Kaptanto — Universal Database CDC' },
    {
      name: 'twitter:description',
      content:
        'Open-source CDC with 8 output modes: stdout, SSE, gRPC, NATS, SQS, Kafka, Pub/Sub, and RabbitMQ.',
    },
    { name: 'twitter:image', content: 'https://kaptanto.dev/logo.png' },
  ],
  links: [
    { rel: 'preconnect', href: 'https://fonts.googleapis.com' },
    { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: 'anonymous' },
    {
      rel: 'stylesheet',
      href: 'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600;700&family=IBM+Plex+Sans:wght@300;400;500;600;700&display=swap',
    },
    { rel: 'stylesheet', href: '/legacy.css' },
    { rel: 'icon', type: 'image/png', href: '/logo.png' },
    { rel: 'alternate', hreflang: 'en', href: 'https://kaptanto.dev/' },
    { rel: 'alternate', hreflang: 'x-default', href: 'https://kaptanto.dev/' },
    { rel: 'sitemap', type: 'application/xml', href: '/sitemap.xml' },
  ],
  scripts: [
    {
      props: { type: 'application/ld+json' },
      script: JSON.stringify(jsonLd),
    },
  ],
};
