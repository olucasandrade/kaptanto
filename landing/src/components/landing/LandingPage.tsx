import { component$ } from '@builder.io/qwik';
import type { Signal } from '@builder.io/qwik';
import { Hero } from '../hero/Hero';
import { Install } from '../install/Install';

interface LandingPageProps {
  currentDoc: Signal<string | null>;
}

export const LandingPage = component$<LandingPageProps>(({ currentDoc }) => {
  return (
    <div id="pg-landing">
      <Hero currentDoc={currentDoc} />

      {/* Features */}
      <section class="sec" id="features">
        <div class="sl sr">Features</div>
        <div class="stt sr">Production CDC. Minus the complexity.</div>
        <div class="sd sr">
          Backfills, crash recovery, per-key ordering, and HA — built in. Nothing extra to operate.
        </div>
        <div class="fg sr">
          <div class="fc">
            <div class="fc-i">latency</div>
            <h3>Sub-millisecond</h3>
            <p>
              Events flow from the WAL as each transaction commits. No polling interval, no artificial
              delay.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">schema</div>
            <h3>One event schema</h3>
            <p>
              The same JSON format across every source. Write your consumer once and connect to any
              database.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">checkpoint</div>
            <h3>Crash-safe cursors</h3>
            <p>
              Per-consumer positions persist on every event. Reconnect and resume from exactly where you
              stopped.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">backfill</div>
            <h3>Consistent backfills</h3>
            <p>
              Snapshot and stream run concurrently. Watermark coordination prevents stale or duplicate
              rows.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">ordering</div>
            <h3>Per-key ordering</h3>
            <p>
              Events for the same primary key always arrive in commit order. Slow consumers never block
              other partitions.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">ha</div>
            <h3>Built-in HA</h3>
            <p>
              Two instances, one leader. Advisory lock election — session-scoped, no clock skew, ~5-second
              failover.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">sinks</div>
            <h3>5 queue sinks</h3>
            <p>
              Push CDC events directly to NATS, SQS, Kafka, Pub/Sub, or RabbitMQ. At-least-once delivery
              with per-key ordering end-to-end.
            </p>
          </div>
          <div class="fc">
            <div class="fc-i">routing</div>
            <h3>Per-table routing</h3>
            <p>
              Route events from different tables to different topics or queues via a Go template —{' '}
              <code style="font-size:.78em">{'cdc.{{.Schema}}.{{.Table}}'}</code>.
            </p>
          </div>
        </div>
      </section>

      {/* Sources */}
      <section class="sec" id="sources">
        <div class="sl sr">Compatibility</div>
        <div class="stt sr">Works with the databases you already run.</div>
        <div class="cr sr">
          <div class="cc">
            <h3>Database sources</h3>
            <div class="ci">
              <div class="ci-i" style="background:rgba(110,125,247,.1);color:var(--bl)">
                PG
              </div>
              <div>
                <div class="ci-n">PostgreSQL</div>
                <div class="ci-d">WAL logical replication · v14-17</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:var(--gm);color:var(--g)">
                MG
              </div>
              <div>
                <div class="ci-n">MongoDB</div>
                <div class="ci-d">Change Streams · v4.0+</div>
              </div>
            </div>
            <div class="ci ci-dim">
              <div class="ci-i" style="background:rgba(255,178,36,.08);color:var(--am)">
                MY
              </div>
              <div>
                <div class="ci-n">
                  MySQL <span style="font-size:.6rem;color:var(--am)">soon</span>
                </div>
                <div class="ci-d">binlog · GTID</div>
              </div>
            </div>
          </div>
          <div class="cc">
            <h3>Output modes</h3>
            <div class="ci">
              <div class="ci-i" style="background:var(--gm);color:var(--g)">
                &gt;
              </div>
              <div>
                <div class="ci-n">stdout</div>
                <div class="ci-d">NDJSON · pipe anywhere</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(255,92,138,.08);color:var(--ro)">
                SE
              </div>
              <div>
                <div class="ci-n">Server-Sent Events</div>
                <div class="ci-d">HTTP · auto-reconnect · Last-Event-ID</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(110,125,247,.1);color:var(--bl)">
                gR
              </div>
              <div>
                <div class="ci-n">gRPC Stream</div>
                <div class="ci-d">Protobuf · HTTP/2 · backpressure</div>
              </div>
            </div>
          </div>
          <div class="cc">
            <h3>
              Queue sinks{' '}
              <span
                style="font-size:.6rem;padding:.1rem .35rem;background:rgba(101,196,140,.1);color:var(--g);border-radius:3px;margin-left:.3rem;vertical-align:middle"
              >
                v0.2.0
              </span>
            </h3>
            <div class="ci">
              <div class="ci-i" style="background:rgba(101,196,140,.08);color:var(--g)">
                NA
              </div>
              <div>
                <div class="ci-n">NATS JetStream</div>
                <div class="ci-d">at-least-once · PubAck · per-table subjects</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(255,178,36,.08);color:var(--am)">
                SQ
              </div>
              <div>
                <div class="ci-n">AWS SQS FIFO</div>
                <div class="ci-d">MessageGroupId ordering · mTLS · per-table queues</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(110,125,247,.1);color:var(--bl)">
                KF
              </div>
              <div>
                <div class="ci-n">Kafka</div>
                <div class="ci-d">franz-go · SASL/TLS · record key ordering</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(255,92,138,.08);color:var(--ro)">
                PS
              </div>
              <div>
                <div class="ci-n">Google Pub/Sub</div>
                <div class="ci-d">ordering key · ResumePublish · TopicTemplate</div>
              </div>
            </div>
            <div class="ci">
              <div class="ci-i" style="background:rgba(255,178,36,.08);color:var(--am)">
                RQ
              </div>
              <div>
                <div class="ci-n">RabbitMQ</div>
                <div class="ci-d">AMQP · publisher confirms · auto-reconnect</div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <Install currentDoc={currentDoc} />

      {/* Compare */}
      <section class="sec" id="compare">
        <div class="sl sr">Why kaptanto</div>
        <div class="stt sr">A complete CDC stack that fits in a single binary.</div>
        <div class="cw sr">
          <table class="cmp">
            <thead>
              <tr>
                <th>Tool</th>
                <th>Real-time</th>
                <th>No Kafka</th>
                <th>Multi-DB</th>
                <th>Single binary</th>
                <th>Free</th>
                <th>Min cost</th>
              </tr>
            </thead>
            <tbody>
              <tr class="hi">
                <td>kaptanto</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td>$0</td>
              </tr>
              <tr>
                <td>Debezium</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ck">✓</td>
                <td>$0+Kafka</td>
              </tr>
              <tr>
                <td>Confluent</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ca">~</td>
                <td>~$200/mo</td>
              </tr>
              <tr>
                <td>Fivetran</td>
                <td class="cx">✗</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ca">~</td>
                <td>$12K/yr</td>
              </tr>
              <tr>
                <td>Estuary</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="ck">✓</td>
                <td>$0</td>
              </tr>
              <tr>
                <td>AWS DMS</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="ck">✓</td>
                <td class="cx">✗</td>
                <td class="cx">✗</td>
                <td>~$70/mo</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      {/* Changelog */}
      <section class="sec" id="changelog">
        <div class="sl sr">Changelog</div>
        <div class="stt sr">What changed and when.</div>
        <div class="chlog sr">
          <div class="chver">
            <div class="chver-h">
              <span class="chtag">v0.2.0</span>
              <span class="chdate">May 2026</span>
              <span class="chname">Queue Sinks</span>
            </div>
            <ul class="chlist">
              <li>
                5 new output sinks: NATS JetStream, AWS SQS FIFO, Kafka, Google Pub/Sub, RabbitMQ —
                select with <code>--output nats|sqs|kafka|pubsub|rabbitmq</code>
              </li>
              <li>
                At-least-once delivery — cursor never advances before the broker acknowledges receipt
                (CHK-01 preserved)
              </li>
              <li>
                Per-key ordering end-to-end: <code>MessageGroupId</code> (SQS), record key (Kafka),
                ordering key (Pub/Sub), subject routing (NATS)
              </li>
              <li>
                Per-table topic/queue routing via Go template:{' '}
                <code>{'cdc.{{.Schema}}.{{.Table}}'}</code> — supported on NATS, SQS, and Pub/Sub
              </li>
              <li>
                SQS: CA pinning and mTLS (CertFile + KeyFile) wired into AWS SDK HTTP transport; startup
                validation for incomplete mTLS config
              </li>
              <li>
                Prometheus metrics (<code>queue_publish_total</code>,{' '}
                <code>queue_publish_errors_total</code>, <code>queue_publish_latency_seconds</code>) and{' '}
                <code>/healthz</code> probe for each active sink
              </li>
              <li>
                RabbitMQ: publisher confirms, 64-partition channel pool, exponential-backoff reconnect
                loop
              </li>
            </ul>
          </div>
          <div class="chver chver-old">
            <div class="chver-h">
              <span class="chtag chtag-old">v0.1.0</span>
              <span class="chdate">Mar 2026</span>
              <span class="chname">Initial Release</span>
            </div>
            <ul class="chlist">
              <li>
                Postgres WAL CDC via pgoutput — insert, update, delete, TOAST handling, schema evolution
              </li>
              <li>
                MongoDB Change Streams — BSON normalization, resume tokens, automatic re-snapshot on
                token expiry
              </li>
              <li>
                Three output modes: stdout NDJSON, SSE with per-consumer cursors and Last-Event-ID, gRPC
                streaming
              </li>
              <li>
                Consistent backfills — keyset cursors, watermark dedup, crash-resumable snapshot
                progress
              </li>
              <li>
                High availability — Postgres advisory lock leader election, ~5s failover, shared
                checkpoint store
              </li>
              <li>
                Distributed mode — NATS JetStream replicated event log, 64-partition active/active
                delivery, epoch fencing
              </li>
              <li>Optional Rust FFI acceleration for pgoutput decoding and JSON serialization</li>
              <li>
                Benchmark suite — Docker Compose harness vs. Debezium, Sequin, PeerDB across 4 scenarios
              </li>
            </ul>
          </div>
        </div>
      </section>

      {/* CTA */}
      <div class="cta sr">
        <h2>Your first event in two minutes.</h2>
        <p>Install kaptanto, point it at your database, and start streaming.</p>
        <div class="cta-b">
          <a href="#install" class="bg">
            Install now
          </a>
          <a
            href="/?doc=docs-intro"
            onClick$={(e) => {
              e.preventDefault();
              currentDoc.value = 'docs-intro';
              window.scrollTo(0, 0);
            }}
            class="bo"
          >
            Read the docs
          </a>
        </div>
      </div>

      {/* Footer */}
      <footer class="ft">
        <div class="fi">
          <div class="fgr">
            <div>
              <div class="fb">
                <img src="/logo.png" alt="Kaptanto logo" class="flg" />
                kaptanto
              </div>
              <p class="fdesc">
                Lightweight CDC for Postgres and MongoDB. Open source, Apache 2.0.
              </p>
              <p class="fesp">"kaptanto" — who captures (Esperanto)</p>
            </div>
            <div class="fcol">
              <h4>Product</h4>
              <a href="#features">Features</a>
              <a href="#sources">Sources</a>
              <a href="#install">Install</a>
              <a href="#compare">Compare</a>
              <a href="#changelog">Changelog</a>
            </div>
            <div class="fcol">
              <h4>Resources</h4>
              <a
                href="/?doc=docs-intro"
                onClick$={(e) => {
                  e.preventDefault();
                  currentDoc.value = 'docs-intro';
                  window.scrollTo(0, 0);
                }}
              >
                Docs
              </a>
              <a
                href="/?doc=docs-quickstart"
                onClick$={(e) => {
                  e.preventDefault();
                  currentDoc.value = 'docs-quickstart';
                  window.scrollTo(0, 0);
                }}
              >
                Quick Start
              </a>
              <a
                href="/?doc=docs-config"
                onClick$={(e) => {
                  e.preventDefault();
                  currentDoc.value = 'docs-config';
                  window.scrollTo(0, 0);
                }}
              >
                Config
              </a>
              <a href="#">Blog</a>
            </div>
            <div class="fcol">
              <h4>Community</h4>
              <a href="https://github.com/olucasandrade/kaptanto">GitHub</a>
              <a href="#">Discord</a>
              <a href="#">X</a>
              <a href="#">Contributing</a>
            </div>
          </div>
          <div class="fbot">
            <span>&copy; 2026 Kaptanto. Apache 2.0 License.</span>
            <span>
              Made in Brazil &middot;{' '}
              <a href="https://github.com/olucasandrade/kaptanto">Source</a>
            </span>
          </div>
        </div>
      </footer>
    </div>
  );
});
