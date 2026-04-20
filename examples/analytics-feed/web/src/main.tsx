import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

type Snapshot = {
  feed: { id: string; type: string; actor: string; workspace: string; createdAt: string }[];
  countsByType: Record<string, number>;
  countsByWorkspace: Record<string, number>;
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4003";

function App() {
  const [data, setData] = useState<Snapshot>({ feed: [], countsByType: {}, countsByWorkspace: {} });
  const [type, setType] = useState("report.exported");
  const [actor, setActor] = useState("ava");
  const [workspace, setWorkspace] = useState("northwind");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((response) => response.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (event) => setData(JSON.parse(event.data));
    return () => stream.close();
  }, []);

  async function createEvent() {
    await fetch(`${apiUrl}/api/events`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ type, actor, workspace }),
    });
  }

  return (
    <div className="page">
      <section className="grid" style={{ marginBottom: 20 }}>
        <h1 style={{ fontFamily: "Space Grotesk, sans-serif", fontSize: "2.8rem", margin: 0 }}>
          Live Activity Feed
        </h1>
        <p>MongoDB change events become admin-facing operational visibility with no polling layer.</p>
      </section>

      <div className="layout">
        <section className="card grid">
          <h2>Generate source events</h2>
          <label>
            Event type
            <select value={type} onChange={(event) => setType(event.target.value)}>
              <option value="report.exported">report.exported</option>
              <option value="comment.created">comment.created</option>
              <option value="workspace.invited">workspace.invited</option>
            </select>
          </label>
          <label>
            Actor
            <input value={actor} onChange={(event) => setActor(event.target.value)} />
          </label>
          <label>
            Workspace
            <input value={workspace} onChange={(event) => setWorkspace(event.target.value)} />
          </label>
          <button onClick={createEvent}>Insert Mongo event</button>
          <div className="stats">
            {Object.entries(data.countsByType).map(([key, value]) => (
              <div className="stat" key={key}>
                <strong>{key}</strong>
                <div>{value} events</div>
              </div>
            ))}
          </div>
        </section>

        <section className="card grid">
          <h2>Recent feed</h2>
          {data.feed.map((row) => (
            <div key={row.id} className="stat">
              <strong>{row.type}</strong>
              <div>
                {row.actor} in {row.workspace}
              </div>
              <small>{new Date(row.createdAt).toLocaleString()}</small>
            </div>
          ))}
        </section>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);

