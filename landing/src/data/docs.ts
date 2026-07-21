export type SeoDoc = {
  slug: string;
  title: string;
  description: string;
};

export const SEO_DOCS: SeoDoc[] = [
  { slug: 'docs-intro', title: 'Introduction', description: 'What Kaptanto is and how CDC works across Postgres and MongoDB.' },
  { slug: 'docs-quickstart', title: 'Quick Start', description: 'Install Kaptanto and stream your first events in minutes.' },
  { slug: 'docs-install', title: 'Installation', description: 'Install on Linux, macOS, Windows via curl, Docker, or from source.' },
  { slug: 'docs-postgres', title: 'Connect Postgres', description: 'Configure WAL logical replication and failover-safe connections.' },
  { slug: 'docs-mongo', title: 'Connect MongoDB', description: 'Use Change Streams with replica sets and resume tokens.' },
  { slug: 'docs-schema', title: 'Event Schema', description: 'Understand Kaptanto event structure and idempotency keys.' },
  { slug: 'docs-backfills', title: 'Backfills', description: 'Snapshot strategies, watermark coordination, and recovery.' },
  { slug: 'docs-consistency', title: 'Consistency Model', description: 'Delivery guarantees, ordering model, and durability semantics.' },
  { slug: 'docs-ordering', title: 'Ordering & Partitions', description: 'Configure message groups and throughput-safe partitioning.' },
  { slug: 'docs-stdout', title: 'stdout Output', description: 'Pipe NDJSON events to local processes and scripts.' },
  { slug: 'docs-sse', title: 'Server-Sent Events', description: 'Multi-consumer HTTP streaming with resumable event IDs.' },
  { slug: 'docs-grpc', title: 'gRPC Output', description: 'High-throughput streaming with protobuf and backpressure.' },
  { slug: 'docs-queue-sinks', title: 'Queue Sinks', description: 'Push CDC events to NATS, SQS, Kafka, Pub/Sub, or RabbitMQ with at-least-once delivery and per-key ordering.' },
  { slug: 'docs-vector', title: 'Vector Streaming & RAG', description: 'Embed CDC row text and upsert into pgvector, Pinecone, or Qdrant for retrieval-augmented generation.' },
  { slug: 'docs-cluster', title: 'Cluster Mode', description: 'Active-active delivery across multiple nodes with embedded NATS JetStream and shared partition ownership.' },
  { slug: 'docs-config', title: 'CLI & YAML Configuration', description: 'Production configuration for sources, output, HA, cluster, and metrics.' },
  { slug: 'docs-filtering', title: 'Filtering', description: 'Filter by table, operation, columns, and row conditions.' },
  { slug: 'docs-grouping', title: 'Message Grouping', description: 'Tune grouping keys for strict ordering and throughput.' },
  { slug: 'docs-ha', title: 'High Availability', description: 'Leader election and automatic failover behavior.' },
  { slug: 'docs-metrics', title: 'Metrics & Monitoring', description: 'Prometheus metrics and health checks for observability.' },
  { slug: 'docs-api', title: 'HTTP Endpoints', description: 'The HTTP endpoints kaptanto serves: /events, /metrics, and /healthz.' },
  { slug: 'docs-troubleshooting', title: 'Troubleshooting', description: 'Fix common CDC issues quickly in production.' },
  { slug: 'docs-actions', title: 'Actions', description: 'Turn CDC events into side effects — Slack, Discord, email, HTTP, cache purges, vector upserts, and workflow triggers.' },
  { slug: 'docs-serverless', title: 'Serverless Actions', description: 'Invoke AWS Lambda, Cloudflare Workers, and Vercel functions from CDC events — cold starts, async invocation, and response handling.' },
  { slug: 'docs-routing', title: 'Routing Rules', description: 'Filter events by table globs, operations, and WHERE conditions with before./after. prefixes.' },
  { slug: 'docs-openapi', title: 'OpenAPI Discovery', description: 'Machine-readable spec of configured actions served at /openapi.json.' },
  { slug: 'docs-mcp', title: 'MCP Server', description: 'Expose live CDC to Claude Desktop and other agents over the Model Context Protocol.' },
  { slug: 'docs-ai-context', title: 'AI Event Contract', description: 'Optional ai_context enrichment attached before the durable Event Log write.' },
  { slug: 'docs-python-sdk', title: 'Python SDK', description: 'pydantic ChangeEvent models and an httpx SSE client — pip install kaptanto.' },
  { slug: 'docs-mastra', title: 'Mastra Integration', description: 'Trigger Mastra workflows from real-time database changes with @kaptanto/mastra.' },
  { slug: 'docs-n8n', title: 'n8n Integration', description: 'Use kaptanto as an n8n trigger node to start workflows on database changes.' },
  { slug: 'docs-triggerdev', title: 'Trigger.dev Integration', description: 'Fire Trigger.dev tasks from CDC events with zero consumer code.' },
  { slug: 'docs-inngest', title: 'Inngest Integration', description: 'Run durable Inngest functions from CDC events with zero consumer code.' },
  { slug: 'docs-aws-setup', title: 'AWS Deployment Guide', description: 'How to run kaptanto, Debezium, and Sequin alongside an API on AWS — infrastructure, cost, and consumer code compared.' },
  { slug: 'docs-benchmarks', title: 'Benchmarks', description: 'Independent throughput and latency comparison of kaptanto vs. Debezium and Sequin across steady, burst, large-batch, crash-recovery, and cluster scenarios.' },
];

export const SEO_DOCS_MAP = new Map(SEO_DOCS.map((d) => [d.slug, d]));
