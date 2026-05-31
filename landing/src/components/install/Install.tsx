import { component$, useSignal } from '@builder.io/qwik';
import type { Signal } from '@builder.io/qwik';

interface InstallProps {
  currentDoc: Signal<string | null>;
}

type Tab = 'curl' | 'docker' | 'brew' | 'src';

export const Install = component$<InstallProps>(({ currentDoc }) => {
  const activeTab = useSignal<Tab>('curl');

  return (
    <div class="ib" id="install">
      <div class="ii">
        <div class="sl sr">Get started</div>
        <div class="stt sr">From install to streaming in 90 seconds.</div>

        <div class="qs sr">
          <div class="qs-step">
            <div class="qs-num">1</div>
            <div class="qs-body">
              <div class="qs-label">Install</div>
              <div style="max-width:540px">
                <div class="its">
                  {(['curl', 'docker', 'brew', 'src'] as Tab[]).map((t) => (
                    <button
                      key={t}
                      class={`it${activeTab.value === t ? ' a' : ''}`}
                      onClick$={() => { activeTab.value = t; }}
                    >
                      {t === 'curl' ? 'curl' : t === 'docker' ? 'Docker' : t === 'brew' ? 'Homebrew' : 'Source'}
                    </button>
                  ))}
                </div>

                {activeTab.value === 'curl' && (
                  <div class="ic">
                    <CopyButton />
                    <span class="tg">$</span> curl -fsSL https://get.kaptan.to | sh
                  </div>
                )}
                {activeTab.value === 'docker' && (
                  <div class="ic">
                    <CopyButton />
                    <span class="tg">$</span> docker run olucasandrade/kaptanto \
                    <br />&nbsp;&nbsp;<span class="tbl">--source</span>{' '}
                    <span class="ty">postgres://localhost:5432/mydb</span> \
                    <br />&nbsp;&nbsp;<span class="tbl">--tables</span>{' '}
                    <span class="ty">orders</span> <span class="tbl">--output</span>{' '}
                    <span class="ty">stdout</span>
                  </div>
                )}
                {activeTab.value === 'brew' && (
                  <div class="ic">
                    <CopyButton />
                    <span class="tg">$</span> brew install kaptanto/tap/kaptanto
                  </div>
                )}
                {activeTab.value === 'src' && (
                  <div class="ic">
                    <CopyButton />
                    <span class="tg">$</span> git clone https://github.com/olucasandrade/kaptanto
                    <br />
                    <span class="tg">$</span> cd kaptanto &amp;&amp; go build -o kaptanto ./cmd/kaptanto
                  </div>
                )}
              </div>
            </div>
          </div>

          <div class="qs-step">
            <div class="qs-num">2</div>
            <div class="qs-body">
              <div class="qs-label">Run — point at your database</div>
              <div class="ic" style="max-width:540px;position:relative">
                <CopyButton />
                <span class="tg">$</span> kaptanto \<br />
                &nbsp;&nbsp;<span class="tbl">--source</span>{' '}
                <span class="ty">postgres://localhost:5432/mydb</span> \<br />
                &nbsp;&nbsp;<span class="tbl">--tables</span>{' '}
                <span class="ty">orders,payments</span> \<br />
                &nbsp;&nbsp;<span class="tbl">--output</span>{' '}
                <span class="ty">stdout</span>
              </div>
              <p class="qs-hint">
                Use <code>--output sse</code> or <code>--output grpc</code> for multi-consumer setups. Use{' '}
                <code>--output nats|sqs|kafka|pubsub|rabbitmq</code> to push directly to a queue.
              </p>
            </div>
          </div>

          <div class="qs-step qs-step-last">
            <div class="qs-num">3</div>
            <div class="qs-body">
              <div class="qs-label">Events stream out — one JSON line per change</div>
              <div class="ic qs-out" style="max-width:540px">
                <span class="to">
                  {'{"op":"insert","table":"orders","after":{"id":1,"status":"pending","amount":49.99}}'}
                </span>
                <br />
                <span class="to">
                  {'{"op":"update","table":"orders","before":{"status":"pending"},"after":{"status":"shipped"}}'}
                </span>
                <br />
                <span class="to">{'{"op":"delete","table":"payments","key":{"id":88}}'}</span>
              </div>
              <p class="qs-hint">
                Pipe to <code>jq</code>, a webhook, a queue, or anything that reads stdin.
              </p>
            </div>
          </div>
        </div>

        <div class="qs-links sr">
          <a
            href="/?doc=docs-quickstart"
            onClick$={(e) => {
              e.preventDefault();
              currentDoc.value = 'docs-quickstart';
              window.scrollTo(0, 0);
            }}
            class="bo"
          >
            Full quick start →
          </a>
          <a
            href="/?doc=docs-config"
            onClick$={(e) => {
              e.preventDefault();
              currentDoc.value = 'docs-config';
              window.scrollTo(0, 0);
            }}
            class="qs-cfg"
          >
            Config reference
          </a>
        </div>
      </div>
    </div>
  );
});

const CopyButton = component$(() => {
  const label = useSignal('copy');

  return (
    <button
      class="cpb"
      onClick$={(e) => {
        const btn = e.target as HTMLButtonElement;
        const text = btn.parentElement?.textContent?.replace('copy', '').replace(/\$ /g, '').trim() ?? '';
        navigator.clipboard.writeText(text).then(() => {
          label.value = 'copied!';
          setTimeout(() => { label.value = 'copy'; }, 1400);
        });
      }}
    >
      {label.value}
    </button>
  );
});
