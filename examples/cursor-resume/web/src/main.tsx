import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type Job = { id: string; title: string; status: string; priority: string };
type ConsumerMeta = {
  status: "connected" | "paused";
  eventsProcessed: number;
  lastEventId: string | null;
  pausedAt: string | null;
};
type Snapshot = { jobs: Job[]; recentEvents: string[]; consumer: ConsumerMeta };

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4005";

const STATUS_ORDER = ["queued", "running", "completed", "failed"];
const PRIORITY_COLORS: Record<string, string> = { high: "pill-high", normal: "pill-normal", low: "pill-low" };

function StatusPill({ status }: { status: string }) {
  const cls = { queued: "pill-queued", running: "pill-running", completed: "pill-done", failed: "pill-failed" }[status] ?? "pill-queued";
  return <span className={`pill ${cls}`}><span className="pill-dot" />{status}</span>;
}

function JobRow({ job, onAdvance }: { job: Job; onAdvance: (id: string, status: string) => void }) {
  const nextIdx = STATUS_ORDER.indexOf(job.status) + 1;
  const next = STATUS_ORDER[nextIdx];

  return (
    <div className={`job-row job-${job.status}`}>
      <div className="job-left">
        <StatusPill status={job.status} />
        <span className={`priority-tag ${PRIORITY_COLORS[job.priority] ?? "pill-normal"}`}>{job.priority}</span>
        <span className="job-title">{job.title}</span>
      </div>
      <div className="job-right">
        <span className="job-id">{job.id.slice(0, 8)}</span>
        {next && next !== "failed" && (
          <button className="btn-sm" onClick={() => onAdvance(job.id, next)}>
            → {next}
          </button>
        )}
      </div>
    </div>
  );
}

function App() {
  const [data, setData] = useState<Snapshot>({ jobs: [], recentEvents: [], consumer: { status: "connected", eventsProcessed: 0, lastEventId: null, pausedAt: null } });
  const [jobTitle, setJobTitle] = useState("Rebuild search index");
  const [priority, setPriority] = useState("normal");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`).then((r) => r.json()).then(setData);
    const es = new EventSource(`${apiUrl}/api/events`);
    es.onmessage = (e) => setData(JSON.parse(e.data));
    return () => es.close();
  }, []);

  async function toggleConsumer() {
    const action = data.consumer.status === "connected" ? "pause" : "resume";
    await fetch(`${apiUrl}/api/consumer/${action}`, { method: "POST" });
  }

  async function addJob() {
    await fetch(`${apiUrl}/api/jobs`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: jobTitle, priority }),
    });
  }

  async function advanceJob(id: string, status: string) {
    await fetch(`${apiUrl}/api/jobs/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ status }),
    });
  }

  const { consumer } = data;
  const isPaused = consumer.status === "paused";

  return (
    <div className="page">
      <header className="header">
        <div className="header-kicker">
          <span className="kicker-dot" />
          Postgres · Kaptanto SSE · Durable consumer cursors
        </div>
        <h1>Cursor Resume</h1>
        <p className="header-desc">
          Pause the consumer, write more jobs, then resume — Kaptanto replays every missed event
          from the saved cursor position. Zero events lost.
        </p>
      </header>

      <div className="layout">
        <div className="sidebar">
          {/* Consumer status */}
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Consumer</span>
              <span className={`status-badge ${isPaused ? "badge-paused" : "badge-live"}`}>
                {isPaused ? "paused" : "live"}
              </span>
            </div>
            <div className="panel-body">
              <div className="stat-grid">
                <div className="stat">
                  <span className="stat-label">Events processed</span>
                  <span className="stat-value mono">{consumer.eventsProcessed}</span>
                </div>
                <div className="stat">
                  <span className="stat-label">Last event ID</span>
                  <span className="stat-value mono small">{consumer.lastEventId?.slice(0, 16) ?? "—"}</span>
                </div>
                {isPaused && (
                  <div className="stat">
                    <span className="stat-label">Paused at</span>
                    <span className="stat-value mono small">{consumer.pausedAt ? new Date(consumer.pausedAt).toLocaleTimeString() : "—"}</span>
                  </div>
                )}
              </div>
              <button
                className={isPaused ? "btn-primary btn-resume" : "btn-danger"}
                onClick={toggleConsumer}
              >
                {isPaused ? "Resume consumer" : "Pause consumer"}
              </button>
              {isPaused && (
                <p className="hint">
                  Write jobs now — Kaptanto queues them. On resume, the cursor catches up automatically.
                </p>
              )}
            </div>
          </div>

          {/* Add job */}
          <div className="panel">
            <div className="panel-header"><span className="panel-title">Add job</span></div>
            <div className="panel-body">
              <div className="form-group">
                <label className="form-label">Job title</label>
                <input value={jobTitle} onChange={(e) => setJobTitle(e.target.value)} />
              </div>
              <div className="form-group">
                <label className="form-label">Priority</label>
                <select value={priority} onChange={(e) => setPriority(e.target.value)}>
                  <option value="high">High</option>
                  <option value="normal">Normal</option>
                  <option value="low">Low</option>
                </select>
              </div>
              <button className="btn-primary" onClick={addJob}>Enqueue job</button>
            </div>
          </div>

          {/* CDC log */}
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">CDC event log</span>
              <span className="panel-count">{data.recentEvents.length}</span>
            </div>
            <div style={{ padding: "10px 16px" }}>
              {data.recentEvents.length === 0 ? (
                <div className="log-empty">No events yet.</div>
              ) : (
                <div className="events-log">
                  {data.recentEvents.map((ev, i) => (
                    <div key={i} className="event-log-row">
                      <span className="log-dot" /><span>{ev}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Job list */}
        <div className="main-panel">
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Jobs</span>
              <span className="panel-count">{data.jobs.length}</span>
            </div>
            <div className="job-list">
              {data.jobs.length === 0 ? (
                <div className="log-empty" style={{ padding: "24px", textAlign: "center" }}>No jobs yet.</div>
              ) : (
                [...data.jobs]
                  .sort((a, b) => STATUS_ORDER.indexOf(a.status) - STATUS_ORDER.indexOf(b.status))
                  .map((job) => <JobRow key={job.id} job={job} onAdvance={advanceJob} />)
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
