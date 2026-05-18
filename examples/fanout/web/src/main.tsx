import { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type SourceProduct = {
  id: string;
  name: string;
  category: string;
  priceCents: number;
  stockQuantity: number;
  description: string;
};

type SearchEntry = { id: string; name: string; category: string; terms: string[] };
type CacheEntry = { id: string; name: string; status: "hot" | "warming"; version: number; refreshedAt: string };
type CacheMutation = { productId: string; name: string; action: string; ts: string };

type Snapshot = {
  source: { products: SourceProduct[]; eventsProcessed: number };
  search: { index: SearchEntry[]; eventsProcessed: number; lastIndexedAt: string | null };
  cache: {
    entries: CacheEntry[];
    eventsProcessed: number;
    refreshes: number;
    invalidations: number;
    recent: CacheMutation[];
  };
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4006";

function fmt(cents: number) {
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);
}

function relative(ts: string) {
  const diff = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000));
  if (diff < 5) return "just now";
  if (diff < 60) return `${diff}s ago`;
  return `${Math.round(diff / 60)}m ago`;
}

function Stat({ label, value, tone = "green" }: { label: string; value: string | number; tone?: "green" | "ink" }) {
  return (
    <div className={`stat stat-${tone}`}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

function App() {
  const [data, setData] = useState<Snapshot>({
    source: { products: [], eventsProcessed: 0 },
    search: { index: [], eventsProcessed: 0, lastIndexedAt: null },
    cache: { entries: [], eventsProcessed: 0, refreshes: 0, invalidations: 0, recent: [] },
  });
  const [query, setQuery] = useState("desk");
  const [productName, setProductName] = useState("Realtime Vector Catalog");
  const [category, setCategory] = useState("Search Infrastructure");
  const [description, setDescription] = useState("vector ready semantic ranking search node");
  const [priceCents, setPriceCents] = useState(12900);
  const [stockQty, setStockQty] = useState(12);

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`).then((r) => r.json()).then(setData);
    const es = new EventSource(`${apiUrl}/api/events`);
    es.onmessage = (e) => setData(JSON.parse(e.data));
    return () => es.close();
  }, []);

  const results = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return data.search.index;
    const parts = normalized.split(/\s+/);
    return data.search.index.filter((entry) => {
      const haystack = `${entry.name} ${entry.category} ${entry.terms.join(" ")}`.toLowerCase();
      return parts.every((part) => haystack.includes(part));
    });
  }, [data.search.index, query]);

  const topMutation = data.cache.recent[0];
  const highlightedId = topMutation?.productId ?? results[0]?.id ?? data.source.products[0]?.id;

  async function addProduct() {
    await fetch(`${apiUrl}/api/products`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: productName, category, priceCents, stockQuantity: stockQty, description }),
    });
  }

  async function patchProduct(id: string, payload: Record<string, string | number>) {
    await fetch(`${apiUrl}/api/products/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
  }

  return (
    <div className="page">
      <header className="hero">
        <div className="eyebrow">
          <span className="live-dot" />
          kaptanto example 01
        </div>
        <div className="hero-grid">
          <div>
            <div className="brand-lockup">
              <div className="brand-mark">k</div>
              <div>
                <div className="brand-name">kaptanto</div>
                <div className="brand-sub">CDC search + cache control room</div>
              </div>
            </div>
            <h1>Change the source row. Watch search and cache react immediately.</h1>
            <p className="hero-copy">
              This app keeps everything on one screen: the source-of-truth table, the derived search index,
              and a cache projection driven by the same Kaptanto stream.
            </p>
          </div>
          <div className="hero-stats">
            <Stat label="source events" value={data.source.eventsProcessed} tone="ink" />
            <Stat label="search updates" value={data.search.eventsProcessed} />
            <Stat label="cache refreshes" value={data.cache.refreshes} />
            <Stat label="cache invalidations" value={data.cache.invalidations} tone="ink" />
          </div>
        </div>
      </header>

      <section className="composer panel">
        <div className="panel-head">
          <div>
            <div className="panel-kicker">Write to Postgres</div>
            <h2>Create or mutate products from the source of truth</h2>
          </div>
          <button className="primary-btn" onClick={addProduct}>Insert source row</button>
        </div>
        <div className="composer-grid">
          <label>
            <span>Name</span>
            <input value={productName} onChange={(e) => setProductName(e.target.value)} />
          </label>
          <label>
            <span>Category</span>
            <input value={category} onChange={(e) => setCategory(e.target.value)} />
          </label>
          <label>
            <span>Price (¢)</span>
            <input type="number" value={priceCents} onChange={(e) => setPriceCents(Number(e.target.value))} />
          </label>
          <label>
            <span>Stock</span>
            <input type="number" value={stockQty} onChange={(e) => setStockQty(Number(e.target.value))} />
          </label>
          <label className="composer-wide">
            <span>Description / searchable terms</span>
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>
        </div>
      </section>

      <section className="main-grid">
        <div className="panel source-panel">
          <div className="panel-head compact">
            <div>
              <div className="panel-kicker">Source rows</div>
              <h2>Products in Postgres</h2>
            </div>
            <div className="head-note">Every button writes to the primary DB first.</div>
          </div>
          <div className="source-list">
            {data.source.products.map((product) => (
              <div key={product.id} className={`source-card ${highlightedId === product.id ? "source-card-active" : ""}`}>
                <div className="source-card-top">
                  <div>
                    <div className="source-name">{product.name}</div>
                    <div className="source-meta">{product.category} · {fmt(product.priceCents)} · stock {product.stockQuantity}</div>
                  </div>
                  <span className="source-pill">db row</span>
                </div>
                <p className="source-desc">{product.description}</p>
                <div className="source-actions">
                  <button className="ghost-btn" onClick={() => patchProduct(product.id, { description: `${product.description} vector semantic ranking` })}>
                    Add search terms
                  </button>
                  <button className="ghost-btn" onClick={() => patchProduct(product.id, { name: `${product.name} Pro` })}>
                    Rename item
                  </button>
                  <button className="ghost-btn" onClick={() => patchProduct(product.id, { category: "Search Infrastructure" })}>
                    Reclassify
                  </button>
                  <button className="ghost-btn" onClick={() => patchProduct(product.id, { stockQuantity: Math.max(0, product.stockQuantity - 4) })}>
                    Update stock
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="stack-col">
          <div className="panel search-panel">
            <div className="panel-head compact">
              <div>
                <div className="panel-kicker">Derived search system</div>
                <h2>Query the live index</h2>
              </div>
              <div className="head-note">
                {data.search.lastIndexedAt ? `last indexed ${relative(data.search.lastIndexedAt)}` : "waiting for first event"}
              </div>
            </div>
            <div className="search-box">
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Try: vector, desk, webcam, semantic ranking"
              />
            </div>
            <div className="result-meta">
              <span>{results.length} matching records</span>
              <span>{data.search.index.length} total indexed</span>
            </div>
            <div className="results-list">
              {results.map((entry) => (
                <div key={entry.id} className={`result-card ${highlightedId === entry.id ? "result-card-active" : ""}`}>
                  <div className="result-top">
                    <div>
                      <div className="result-name">{entry.name}</div>
                      <div className="result-cat">{entry.category}</div>
                    </div>
                    <span className="source-pill source-pill-green">indexed</span>
                  </div>
                  <div className="term-row">
                    {entry.terms.slice(0, 8).map((term) => (
                      <span key={term} className="term-chip">{term}</span>
                    ))}
                  </div>
                </div>
              ))}
              {results.length === 0 && <div className="empty-card">No match yet. Change a product description or category and the result set will update when CDC lands.</div>}
            </div>
          </div>

          <div className="panel cache-panel">
            <div className="panel-head compact">
              <div>
                <div className="panel-kicker">Derived cache service</div>
                <h2>Cache warmed by the same stream</h2>
              </div>
              <div className="head-note">{data.cache.eventsProcessed} cache events processed</div>
            </div>
            <div className="cache-grid">
              <div className="cache-status-list">
                {data.cache.entries.slice(0, 8).map((entry) => (
                  <div key={entry.id} className={`cache-card ${highlightedId === entry.id ? "cache-card-active" : ""}`}>
                    <div>
                      <div className="cache-name">{entry.name}</div>
                      <div className="cache-meta">version {entry.version} · {relative(entry.refreshedAt)}</div>
                    </div>
                    <span className={`cache-state cache-state-${entry.status}`}>{entry.status}</span>
                  </div>
                ))}
              </div>
              <div className="cache-feed">
                <div className="feed-title">Recent cache mutations</div>
                {data.cache.recent.map((item, idx) => (
                  <div key={`${item.productId}-${idx}`} className="feed-row">
                    <span className="feed-action">{item.action}</span>
                    <span className="feed-name">{item.name}</span>
                    <span className="feed-time">{relative(item.ts)}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
