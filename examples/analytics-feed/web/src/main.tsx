import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

type FeedRow = {
  id: string;
  type: string;
  actor: string;
  workspace: string;
  createdAt: string;
};

type Snapshot = {
  feed: FeedRow[];
  countsByType: Record<string, number>;
  countsByWorkspace: Record<string, number>;
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4003";

const EVENT_TYPES = [
  "report.exported",
  "comment.created",
  "workspace.invited",
  "workspace.created",
  "user.signup",
  "dashboard.viewed",
  "export.scheduled",
];

const WORKSPACES = ["northwind", "beacon-labs", "pacific-rim", "vertex-io", "atlas-co"];

function getIconClass(type: string): string {
  if (type.startsWith("report") || type.startsWith("export") || type.startsWith("dashboard")) return "type-report";
  if (type.startsWith("comment")) return "type-comment";
  if (type.includes("invite")) return "type-invite";
  if (type.startsWith("workspace")) return "type-workspace";
  if (type.startsWith("user")) return "type-user";
  return "type-default";
}

function getIconLabel(type: string): string {
  if (type.startsWith("report") || type.startsWith("export")) return "EX";
  if (type.startsWith("dashboard")) return "DB";
  if (type.startsWith("comment")) return "CM";
  if (type.includes("invite")) return "IV";
  if (type.includes("create")) return "WS";
  if (type.startsWith("user")) return "US";
  return "EV";
}

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return new Date(iso).toLocaleDateString();
}

function App() {
  const [data, setData] = useState<Snapshot>({ feed: [], countsByType: {}, countsByWorkspace: {} });
  const [type, setType] = useState("report.exported");
  const [actor, setActor] = useState("ava.martin");
  const [workspace, setWorkspace] = useState("northwind");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((r) => r.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (e) => setData(JSON.parse(e.data));
    return () => stream.close();
  }, []);

  async function createEvent() {
    await fetch(`${apiUrl}/api/events`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ type, actor, workspace }),
    });
  }

  const typeEntries = Object.entries(data.countsByType).sort((a, b) => b[1] - a[1]);
  const wsEntries = Object.entries(data.countsByWorkspace).sort((a, b) => b[1] - a[1]);

  return (
    <div className="page">
      <header className="header">
        <div className="header-kicker">
          <span className="kicker-dot" />
          MongoDB · Kaptanto SSE · Change Streams
        </div>
        <h1>Live Activity Feed</h1>
        <p className="header-desc">
          MongoDB change events become admin-facing operational visibility with no polling layer.
          Every document insert flows through Kaptanto CDC into the projection.
        </p>
      </header>

      <div className="layout">
        {/* Left panel */}
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Generate event</span>
            </div>
            <div className="panel-body">
              <div className="form-group">
                <label className="form-label">Event type</label>
                <select value={type} onChange={(e) => setType(e.target.value)}>
                  {EVENT_TYPES.map((t) => (
                    <option key={t} value={t}>{t}</option>
                  ))}
                </select>
              </div>
              <div className="form-group">
                <label className="form-label">Actor</label>
                <input value={actor} onChange={(e) => setActor(e.target.value)} />
              </div>
              <div className="form-group">
                <label className="form-label">Workspace</label>
                <select value={workspace} onChange={(e) => setWorkspace(e.target.value)}>
                  {WORKSPACES.map((w) => (
                    <option key={w} value={w}>{w}</option>
                  ))}
                </select>
              </div>
              <button onClick={createEvent}>Insert MongoDB event</button>
            </div>
          </div>

          {typeEntries.length > 0 && (
            <div className="panel">
              <div className="panel-header">
                <span className="panel-title">Events by type</span>
                <span className="panel-subtitle">{Object.values(data.countsByType).reduce((a, b) => a + b, 0)} total</span>
              </div>
              <div style={{ padding: "12px 16px" }}>
                <div className="counters">
                  {typeEntries.slice(0, 6).map(([k, v], i) => (
                    <div key={k} className={`counter-card type-${i % 5}`}>
                      <div className="counter-value">{v}</div>
                      <div className="counter-label">{k}</div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {wsEntries.length > 0 && (
            <div className="panel">
              <div className="panel-header">
                <span className="panel-title">By workspace</span>
              </div>
              <div style={{ padding: "8px 16px 12px" }}>
                <div className="ws-section">
                  {wsEntries.map(([ws, count]) => (
                    <div key={ws} className="ws-row">
                      <span className="ws-name">{ws}</span>
                      <span className="ws-count">{count}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}
        </div>

        {/* Feed panel */}
        <div className="panel">
          <div className="panel-header">
            <span className="panel-title">Event feed</span>
            <span className="panel-subtitle">{data.feed.length} events</span>
          </div>
          {data.feed.length === 0 ? (
            <div className="feed-empty">No events captured yet.</div>
          ) : (
            <div className="feed-list">
              {data.feed.map((row) => (
                <div key={row.id} className="feed-row">
                  <div className={`feed-icon ${getIconClass(row.type)}`}>
                    {getIconLabel(row.type)}
                  </div>
                  <div className="feed-content">
                    <div className="feed-type">{row.type}</div>
                    <div className="feed-meta">
                      <span className="feed-actor">{row.actor}</span>
                      <span className="feed-sep">·</span>
                      <span className="feed-workspace">{row.workspace}</span>
                    </div>
                  </div>
                  <div className="feed-time">{timeAgo(row.createdAt)}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
