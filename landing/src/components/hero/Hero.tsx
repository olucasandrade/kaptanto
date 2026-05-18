import { component$, useSignal, useVisibleTask$ } from '@builder.io/qwik';
import type { Signal } from '@builder.io/qwik';

interface HeroProps {
  currentDoc: Signal<string | null>;
}

const STREAM_EVENTS = [
  { cls: 'si', op: 'INSERT', name: 'orders #4821' },
  { cls: 'su', op: 'UPDATE', name: 'users #119' },
  { cls: 'si', op: 'INSERT', name: 'payments #7703' },
  { cls: 'sd', op: 'DELETE', name: 'sessions #3321' },
  { cls: 'su', op: 'UPDATE', name: 'orders #4822' },
  { cls: 'si', op: 'INSERT', name: 'invoices #902' },
  { cls: 'su', op: 'UPDATE', name: 'inventory #445' },
  { cls: 'sd', op: 'DELETE', name: 'tokens #8812' },
  { cls: 'si', op: 'INSERT', name: 'audit_log #15590' },
  { cls: 'su', op: 'UPDATE', name: 'accounts #77' },
  { cls: 'si', op: 'INSERT', name: 'transfers #3320' },
  { cls: 'su', op: 'UPDATE', name: 'shipments #663' },
] as const;

const DOUBLED = [...STREAM_EVENTS, ...STREAM_EVENTS];

export const Hero = component$<HeroProps>(({ currentDoc }) => {
  const glitchMounted = useSignal(false);

  useVisibleTask$(() => {
    glitchMounted.value = true;
  });

  const h1Text = 'Turn every database write into a real-time event.';

  return (
    <>
      <div class="hw">
        <div class="ha sr">
          <img src="/logo.png" alt="Kaptanto logo" />
          <span />
          Open source — Apache 2.0 — v0.2.0
        </div>
        <h1 class="sr" style="position:relative">
          Turn every database write into a <em>real-time event.</em>
          {glitchMounted.value && (
            <>
              <span class="glitch-layer glitch-layer-1" aria-hidden="true">{h1Text}</span>
              <span class="glitch-layer glitch-layer-2" aria-hidden="true">{h1Text}</span>
            </>
          )}
        </h1>
        <p class="hs sr">
          kaptanto captures every insert, update, and delete from Postgres and MongoDB the moment it
          happens — and delivers it via stdout, SSE, gRPC, or directly into NATS, SQS, Kafka, Pub/Sub,
          and RabbitMQ. One static binary. Self-contained. Deploys anywhere.
        </p>
        <div class="hact sr">
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
        <div class="ht sr">
          <div class="tb">
            <span class="td" />
            <span class="td" />
            <span class="td" />
            <span class="tl">kaptanto</span>
          </div>
          <div class="tt">
            <div class="tln">
              <span class="tc"># stream order changes to your services</span>
            </div>
            <div class="tln">
              <span class="tg">$</span>{' '}
              <span class="tw">kaptanto</span>{' '}
              <span class="tbl">--source</span>{' '}
              <span class="ty">postgres://prod:5432/fintech</span>{' '}
              <span class="tw">\</span>
            </div>
            <div class="tln" style="padding-left:1.1rem">
              <span class="tbl">--tables</span>{' '}
              <span class="ty">orders,payments</span>{' '}
              <span class="tbl">--output</span>{' '}
              <span class="ty">stdout</span>
            </div>
            <div class="tln" style="margin-top:.35rem">
              <span class="to">
                {'{"op":"insert","table":"orders","after":{"id":1234,"status":"pending","amount":149.90}}'}
              </span>
            </div>
            <div class="tln">
              <span class="to">
                {'{"op":"update","table":"orders","after":{"id":1234,"status":"settled","amount":149.90}}'}
              </span>
            </div>
            <div class="tln">
              <span class="to">
                {'{"op":"insert","table":"payments","after":{"id":5678,"order_id":1234,"method":"pix"}}'}
              </span>
            </div>
          </div>
        </div>
      </div>
      <div class="sb">
        <div class="st" id="stk">
          {DOUBLED.map((ev, i) => (
            <span key={i} class={`se${ev.cls === 'sd' ? ' se-d' : ''}`}>
              <span class={ev.cls}>{ev.op}</span>
              <span class="sn">{ev.name}</span>
            </span>
          ))}
        </div>
      </div>
    </>
  );
});
