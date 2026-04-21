export type SeoDoc = {
  slug: string;
  title: string;
  description: string;
};

export const SEO_DOCS: SeoDoc[] = [
  { slug: 'docs-intro', title: 'Introduction', description: 'What Kaptanto is and how CDC works across Postgres and MongoDB.' },
  { slug: 'docs-quickstart', title: 'Quick Start', description: 'Install Kaptanto and stream your first events in minutes.' },
  { slug: 'docs-install', title: 'Installation', description: 'Install on Linux, macOS, Windows, Docker, and Homebrew.' },
  { slug: 'docs-postgres', title: 'Connect Postgres', description: 'Configure WAL logical replication and failover-safe connections.' },
  { slug: 'docs-mongo', title: 'Connect MongoDB', description: 'Use Change Streams with replica sets and resume tokens.' },
  { slug: 'docs-schema', title: 'Event Schema', description: 'Understand Kaptanto event structure and idempotency keys.' },
  { slug: 'docs-backfills', title: 'Backfills', description: 'Snapshot strategies, watermark coordination, and recovery.' },
  { slug: 'docs-consistency', title: 'Consistency Model', description: 'Delivery guarantees, ordering model, and durability semantics.' },
  { slug: 'docs-ordering', title: 'Ordering & Partitions', description: 'Configure message groups and throughput-safe partitioning.' },
  { slug: 'docs-stdout', title: 'stdout Output', description: 'Pipe NDJSON events to local processes and scripts.' },
  { slug: 'docs-sse', title: 'Server-Sent Events', description: 'Multi-consumer HTTP streaming with resumable event IDs.' },
  { slug: 'docs-grpc', title: 'gRPC Output', description: 'High-throughput streaming with protobuf and backpressure.' },
  { slug: 'docs-config', title: 'CLI & YAML Configuration', description: 'Production configuration for sources, output, HA and metrics.' },
  { slug: 'docs-filtering', title: 'Filtering', description: 'Filter by table, operation, columns, and row conditions.' },
  { slug: 'docs-grouping', title: 'Message Grouping', description: 'Tune grouping keys for strict ordering and throughput.' },
  { slug: 'docs-ha', title: 'High Availability', description: 'Leader election and automatic failover behavior.' },
  { slug: 'docs-metrics', title: 'Metrics & Monitoring', description: 'Prometheus metrics and health checks for observability.' },
  { slug: 'docs-api', title: 'Management API', description: 'Programmatic management of sources, tables and backfills.' },
  { slug: 'docs-troubleshooting', title: 'Troubleshooting', description: 'Fix common CDC issues quickly in production.' },
  { slug: 'docs-guides', title: 'Language Guides', description: 'Consumer implementation patterns by language and runtime.' },
  { slug: 'docs-aws-setup', title: 'AWS Deployment Guide', description: 'How to run kaptanto, Debezium, and Sequin alongside an API on AWS — infrastructure, cost, and consumer code compared.' },
  { slug: 'docs-benchmarks', title: 'Benchmarks', description: 'Independent throughput and latency comparison of kaptanto vs. Debezium and Sequin across steady, burst, large-batch, and crash-recovery scenarios.' },
];

export const SEO_DOCS_MAP = new Map(SEO_DOCS.map((d) => [d.slug, d]));
