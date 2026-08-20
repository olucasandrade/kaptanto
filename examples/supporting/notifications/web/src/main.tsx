import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

type User = { id: string; name: string };

type Notification = {
  id: string;
  title: string;
  body: string;
  createdAt: string;
};

type Snapshot = {
  users: User[];
  notificationsByUser: Record<string, Notification[]>;
  unreadByUser: Record<string, number>;
  activity: string[];
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4001";

const AVATAR_CLASSES: Record<string, string> = {
  ava: "avatar-ava",
  jules: "avatar-jules",
  morgan: "avatar-morgan",
  priya: "avatar-priya",
  sam: "avatar-sam",
};

function getInitials(name: string): string {
  return name
    .split(" ")
    .map((p) => p[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function getNotifIcon(title: string): string {
  if (title.includes("mentioned")) return "@";
  if (title.includes("followed")) return "+";
  return "●";
}

function Avatar({ userId, name }: { userId: string; name: string }) {
  const cls = AVATAR_CLASSES[userId] ?? "avatar-ava";
  return <div className={`avatar ${cls}`}>{getInitials(name)}</div>;
}

function App() {
  const [data, setData] = useState<Snapshot>({
    users: [],
    notificationsByUser: {},
    unreadByUser: {},
    activity: [],
  });
  const [authorId, setAuthorId] = useState("ava");
  const [mentionedUserId, setMentionedUserId] = useState("jules");
  const [body, setBody] = useState("Can you take a look at the rollout plan before EOD?");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((r) => r.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (e) => setData(JSON.parse(e.data));
    return () => stream.close();
  }, []);

  async function createComment() {
    await fetch(`${apiUrl}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ authorId, mentionedUserId, body, postId: "roadmap-q2" }),
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

  const userMap = Object.fromEntries(data.users.map((u) => [u.id, u]));

  return (
    <div className="page">
      <header className="header">
        <div className="header-kicker">
          <span className="kicker-dot" />
          Postgres · Kaptanto SSE · App Projection
        </div>
        <h1>Real-time Notification Inbox</h1>
        <p className="header-desc">
          Comments and follows are written to Postgres once. CDC turns them into user-facing
          notifications without polling or cron reconciliation.
        </p>
      </header>

      <div className="layout">
        {/* Left column — controls */}
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Trigger source writes</span>
            </div>
            <div className="panel-body">
              <div className="form-group">
                <label className="form-label">Actor</label>
                <select value={authorId} onChange={(e) => setAuthorId(e.target.value)}>
                  {data.users.map((u) => (
                    <option key={u.id} value={u.id}>{u.name}</option>
                  ))}
                </select>
              </div>
              <div className="form-group">
                <label className="form-label">Mention / follow target</label>
                <select value={mentionedUserId} onChange={(e) => setMentionedUserId(e.target.value)}>
                  {data.users.map((u) => (
                    <option key={u.id} value={u.id}>{u.name}</option>
                  ))}
                </select>
              </div>
              <div className="form-group">
                <label className="form-label">Comment body</label>
                <textarea
                  rows={3}
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                />
              </div>
              <div className="btn-row">
                <button className="btn-primary" onClick={createComment}>
                  @ Mention in comment
                </button>
                <button className="btn-primary" onClick={createFollow} style={{ background: "var(--bg3)", color: "var(--text2)", border: "1px solid var(--border-mid)", boxShadow: "none" }}>
                  + Follow user
                </button>
              </div>
            </div>
          </div>

          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Activity stream</span>
              <span style={{ fontFamily: "JetBrains Mono, monospace", fontSize: "0.62rem", color: "var(--text3)" }}>
                {data.activity.length} events
              </span>
            </div>
            <div style={{ padding: "10px 16px" }}>
              {data.activity.length === 0 ? (
                <div className="activity-empty">No activity yet — trigger a write above.</div>
              ) : (
                <div className="activity-list">
                  {data.activity.map((item, i) => (
                    <div key={i} className="activity-row">
                      <span className="activity-dot" />
                      <span>{item}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Right column — inbox */}
        <div className="inbox-wrapper">
          {data.users.length === 0 ? (
            <div className="panel" style={{ padding: "32px", textAlign: "center", color: "var(--text3)", fontSize: "0.8rem" }}>
              Loading…
            </div>
          ) : (
            data.users.map((user) => {
              const notifs = data.notificationsByUser[user.id] ?? [];
              const unread = data.unreadByUser[user.id] ?? 0;
              return (
                <div key={user.id} className="user-inbox">
                  <div className="user-inbox-header">
                    <Avatar userId={user.id} name={user.name} />
                    <span className="user-name">{user.name}</span>
                    {unread > 0 && <span className="unread-badge">{unread}</span>}
                    <button
                      className="btn-ghost"
                      onClick={() => markRead(user.id)}
                      style={{ marginLeft: "auto" }}
                    >
                      Mark read
                    </button>
                  </div>
                  {notifs.length === 0 ? (
                    <div className="empty-inbox">No notifications yet</div>
                  ) : (
                    <div className="notifications-list">
                      {notifs.map((n, idx) => (
                        <div
                          key={n.id}
                          className={`notif-row${idx < unread ? " unread" : ""}`}
                        >
                          <div className="notif-icon">
                            {getNotifIcon(n.title)}
                          </div>
                          <div className="notif-content">
                            <div className="notif-title">{n.title}</div>
                            <div className="notif-body">{n.body}</div>
                            <div className="notif-time">{timeAgo(n.createdAt)}</div>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
