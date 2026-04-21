import { component$ } from "@builder.io/qwik";
import type { DocumentHead } from "@builder.io/qwik-city";
import landingBody from "../content/landing-body.html?raw";

const jsonLd = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebSite",
      name: "Kaptanto",
      url: "https://kaptanto.dev/",
      inLanguage: "en",
    },
    {
      "@type": "Organization",
      name: "Kaptanto",
      url: "https://kaptanto.dev",
      logo: "https://kaptanto.dev/logo.png",
      sameAs: ["https://github.com/kaptanto/kaptanto"],
    },
    {
      "@type": "SoftwareApplication",
      name: "Kaptanto",
      applicationCategory: "DeveloperApplication",
      operatingSystem: "Linux, macOS, Windows",
      description:
        "Universal change data capture for Postgres and MongoDB with real-time event streaming.",
      license: "https://opensource.org/licenses/MIT",
      downloadUrl: "https://get.kaptan.to",
      softwareVersion: "0.1.0",
    },
    {
      "@type": "FAQPage",
      mainEntity: [
        {
          "@type": "Question",
          name: "Does Kaptanto require Kafka?",
          acceptedAnswer: {
            "@type": "Answer",
            text: "No. Kaptanto can run as a standalone binary without Kafka.",
          },
        },
        {
          "@type": "Question",
          name: "Which databases are supported?",
          acceptedAnswer: {
            "@type": "Answer",
            text: "Kaptanto supports Postgres and MongoDB.",
          },
        },
      ],
    },
  ],
};

export default component$(() => {
  return <div dangerouslySetInnerHTML={landingBody} />;
});

export const head: DocumentHead = {
  title: "Kaptanto — Universal Database CDC",
  meta: [
    { name: "author", content: "Kaptanto" },
    { name: "application-name", content: "Kaptanto" },
    { name: "theme-color", content: "#050505" },
    { name: "format-detection", content: "telephone=no" },
    {
      name: "description",
      content:
        "Open-source universal CDC for Postgres and MongoDB. Stream real-time database changes via stdout, SSE, or gRPC with one lightweight binary.",
    },
    {
      name: "robots",
      content: "index,follow,max-image-preview:large,max-snippet:-1",
    },
    {
      name: "keywords",
      content:
        "change data capture, cdc postgres, cdc mongodb, realtime database events, open source cdc, wal logical replication",
    },
    { property: "og:type", content: "website" },
    { property: "og:locale", content: "en_US" },
    { property: "og:site_name", content: "Kaptanto" },
    { property: "og:title", content: "Kaptanto — Universal Database CDC" },
    {
      property: "og:description",
      content:
        "Stream every insert, update, and delete from Postgres and MongoDB with one binary.",
    },
    { property: "og:url", content: "https://kaptanto.dev/" },
    { property: "og:image", content: "https://kaptanto.dev/logo.png" },
    { property: "og:image:alt", content: "Kaptanto logo" },
    { property: "og:image:type", content: "image/png" },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:site", content: "@kaptanto" },
    { name: "twitter:title", content: "Kaptanto — Universal Database CDC" },
    {
      name: "twitter:description",
      content:
        "Open-source CDC with real-time streaming outputs: stdout, SSE, and gRPC.",
    },
    { name: "twitter:image", content: "https://kaptanto.dev/logo.png" },
  ],
  links: [
    { rel: "preconnect", href: "https://fonts.googleapis.com" },
    { rel: "preconnect", href: "https://fonts.gstatic.com", crossorigin: "anonymous" },
    {
      rel: "stylesheet",
      href: "https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600;700&family=IBM+Plex+Sans:wght@300;400;500;600;700&display=swap",
    },
    { rel: "stylesheet", href: "/legacy.css" },
    { rel: "icon", type: "image/png", href: "/logo.png" },
    { rel: "alternate", hreflang: "en", href: "https://kaptanto.dev/" },
    { rel: "alternate", hreflang: "x-default", href: "https://kaptanto.dev/" },
    { rel: "sitemap", type: "application/xml", href: "/sitemap.xml" },
  ],
  scripts: [
    { props: { src: "/legacy.js", defer: true } },
    {
      props: { type: "application/ld+json" },
      script: JSON.stringify(jsonLd),
    },
  ],
};
