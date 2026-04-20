import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

type Snapshot = {
  users: { id: string; name: string }[];
  notificationsByUser: Record<string, { id: string; title: string; body: string; createdAt: string }[]>;
  unreadByUser: Record<string, number>;
  activity: string[];
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4001";

function App() {
  const [data, setData] = useState<Snapshot>({
    users: [],
    notificationsByUser: {},
    unreadByUser: {},
    activity: [],
  });
  const [authorId, setAuthorId] = useState("ava");
  const [mentionedUserId, setMentionedUserId] = useState("jules");
  const [body, setBody] = useState("Ship it after legal review.");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((response) => response.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (event) => setData(JSON.parse(event.data));
    return () => stream.close();
  }, []);

  async function createComment() {
    await fetch(`${apiUrl}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ authorId, mentionedUserId, body, postId: "roadmap-42" }),
    });
  }

  async function createFollow() {
    await fetch(`${apiUrl}/api/follows`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ followerId: authorId, targetUserId: mentionedUserId }),
    });
  }

  async function markRead(userId: string) {
    await fetch(`${apiUrl}/api/notifications/read`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ userId }),
    });
  }

  return (
    <div className="page">
      <section className="hero">
        <span className="pill">Postgres to Kaptanto SSE to App Projection</span>
        <h1>Real-time Notification Inbox</h1>
        <p>
          Comments and follows are written once. CDC turns them into user-facing notifications without
          polling or cron reconciliation.
        </p>
      </section>

      <div className="layout">
        <div className="grid">
          <section className="card grid">
            <h2>Trigger source writes</h2>
            <label>
              Actor
              <select value={authorId} onChange={(event) => setAuthorId(event.target.value)}>
                {data.users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Mention / target user
              <select value={mentionedUserId} onChange={(event) => setMentionedUserId(event.target.value)}>
                {data.users.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Comment body
              <textarea rows={4} value={body} onChange={(event) => setBody(event.target.value)} />
            </label>
            <div style={{ display: "flex", gap: 12 }}>
              <button onClick={createComment}>Create comment + mention</button>
              <button onClick={createFollow}>Create follow</button>
            </div>
          </section>

          <section className="card">
            <h2>Activity stream</h2>
            <div className="grid">
              {data.activity.map((item) => (
                <div key={item}>{item}</div>
              ))}
            </div>
          </section>
        </div>

        <section className="card grid">
          <h2>Inbox by user</h2>
          {data.users.map((user) => (
            <div key={user.id} className="grid">
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                <strong>
                  {user.name} · unread {data.unreadByUser[user.id] ?? 0}
                </strong>
                <button onClick={() => markRead(user.id)}>Mark all read</button>
              </div>
              {(data.notificationsByUser[user.id] ?? []).map((notification) => (
                <div key={notification.id} className="notification">
                  <strong>{notification.title}</strong>
                  <div>{notification.body}</div>
                  <small>{new Date(notification.createdAt).toLocaleString()}</small>
                </div>
              ))}
            </div>
          ))}
        </section>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
