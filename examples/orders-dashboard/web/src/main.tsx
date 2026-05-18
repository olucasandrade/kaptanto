import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";

type Order = {
  id: string;
  customerName: string;
  totalCents: number;
  orderStatus: string;
  paymentStatus: string;
  shipmentStatus: string;
};

type Snapshot = {
  orders: Order[];
  recentEvents: string[];
};

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4002";

function formatCents(cents: number): string {
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);
}

function getInitials(name: string): string {
  return name
    .split(" ")
    .map((p) => p[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

type PillVariant =
  | "created" | "pending" | "captured" | "packed"
  | "delivered" | "completed" | "none" | "failed";

function statusToPill(status: string): PillVariant {
  const map: Record<string, PillVariant> = {
    created: "created",
    pending: "pending",
    captured: "captured",
    packed: "packed",
    delivered: "delivered",
    completed: "completed",
    not_created: "none",
    failed: "failed",
  };
  return map[status] ?? "none";
}

function statusLabel(status: string): string {
  const map: Record<string, string> = {
    created: "created",
    pending: "pending",
    captured: "captured",
    packed: "packing",
    delivered: "delivered",
    completed: "done",
    not_created: "—",
    failed: "failed",
  };
  return map[status] ?? status;
}

function StatusPill({ status }: { status: string }) {
  const variant = statusToPill(status);
  return (
    <span className={`pill pill-${variant}`}>
      <span className="pill-dot" />
      {statusLabel(status)}
    </span>
  );
}

function TicketCard({ order, onPayment, onShipment }: {
  order: Order;
  onPayment: (id: string) => void;
  onShipment: (id: string) => void;
}) {
  const initials = getInitials(order.customerName);
  const shortId = order.id.replace("ord-", "").slice(0, 8);

  return (
    <div className="ticket">
      <div className="ticket-top">
        <div className="ticket-avatar">{initials}</div>
        <div className="ticket-info">
          <div className="ticket-name">{order.customerName}</div>
          <div className="ticket-id">#{shortId}</div>
        </div>
        <div className="ticket-amount">{formatCents(order.totalCents)}</div>
      </div>

      <div className="status-grid">
        <div className="status-row">
          <span className="status-key">Order</span>
          <StatusPill status={order.orderStatus} />
        </div>
        <div className="status-row">
          <span className="status-key">Payment</span>
          <StatusPill status={order.paymentStatus} />
        </div>
        <div className="status-row">
          <span className="status-key">Shipment</span>
          <StatusPill status={order.shipmentStatus} />
        </div>
      </div>

      <div className="ticket-actions">
        {order.paymentStatus === "pending" && (
          <button className="btn-sm" onClick={() => onPayment(order.id)}>
            Capture payment
          </button>
        )}
        {order.shipmentStatus === "not_created" && (
          <button className="btn-sm" onClick={() => onShipment(order.id)}>
            Create shipment
          </button>
        )}
        {order.shipmentStatus === "packed" && (
          <button className="btn-sm" onClick={() => onShipment(order.id)}>
            Mark delivered
          </button>
        )}
      </div>
    </div>
  );
}

function App() {
  const [data, setData] = useState<Snapshot>({ orders: [], recentEvents: [] });
  const [customerName, setCustomerName] = useState("Meridian Technologies");
  const [totalCents, setTotalCents] = useState(24900);

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((r) => r.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (e) => setData(JSON.parse(e.data));
    return () => stream.close();
  }, []);

  async function createOrder() {
    await fetch(`${apiUrl}/api/orders`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ customerName, totalCents }),
    });
  }

  async function createPayment(orderId: string) {
    const order = data.orders.find((o) => o.id === orderId);
    await fetch(`${apiUrl}/api/payments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ orderId, amountCents: order?.totalCents ?? totalCents, status: "captured" }),
    });
  }

  async function createShipment(orderId: string) {
    const order = data.orders.find((o) => o.id === orderId);
    const nextStatus = order?.shipmentStatus === "packed" ? "delivered" : "packed";
    await fetch(`${apiUrl}/api/shipments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ orderId, carrier: "DHL Express", status: nextStatus }),
    });
  }

  const lanes = {
    created: data.orders.filter((o) => o.shipmentStatus === "not_created" && o.orderStatus !== "completed"),
    processing: data.orders.filter((o) => o.shipmentStatus === "packed"),
    complete: data.orders.filter((o) => o.orderStatus === "completed" || o.shipmentStatus === "delivered"),
  };

  return (
    <div className="page">
      <header className="header">
        <div className="header-kicker">
          <span className="kicker-dot" />
          Postgres · Kaptanto SSE · Multi-table CDC
        </div>
        <h1>Live Order Operations</h1>
        <p className="header-desc">
          CDC keeps the operational view in sync as orders, payments, and shipments evolve across
          three independent tables — no polling, no joins at query time.
        </p>
      </header>

      <div className="layout">
        {/* Left panel */}
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Create order</span>
            </div>
            <div className="panel-body">
              <div className="form-group">
                <label className="form-label">Customer</label>
                <input
                  value={customerName}
                  onChange={(e) => setCustomerName(e.target.value)}
                  placeholder="Company or customer name"
                />
              </div>
              <div className="form-group">
                <label className="form-label">Amount (cents)</label>
                <input
                  type="number"
                  value={totalCents}
                  onChange={(e) => setTotalCents(Number(e.target.value))}
                />
              </div>
              <button className="btn-primary" onClick={createOrder}>
                Place order
              </button>
            </div>
          </div>

          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">CDC event log</span>
              <span style={{ fontFamily: "JetBrains Mono, monospace", fontSize: "0.62rem", color: "var(--text3)" }}>
                {data.recentEvents.length} recent
              </span>
            </div>
            <div style={{ padding: "10px 16px" }}>
              {data.recentEvents.length === 0 ? (
                <div className="log-empty">No CDC events yet.</div>
              ) : (
                <div className="events-log">
                  {data.recentEvents.map((ev, i) => (
                    <div key={i} className="event-log-row">
                      <span className="log-dot" />
                      <span>{ev}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Board */}
        <div className="board">
          {(
            [
              { key: "created" as const, title: "New orders", lane: lanes.created },
              { key: "processing" as const, title: "Packing", lane: lanes.processing },
              { key: "complete" as const, title: "Complete", lane: lanes.complete },
            ] as const
          ).map(({ key, title, lane }) => (
            <div key={key} className="lane">
              <div className="lane-header">
                <span className="lane-title">{title}</span>
                <span className="lane-count">{lane.length}</span>
              </div>
              <div className="lane-body">
                {lane.length === 0 ? (
                  <div className="lane-empty">Empty</div>
                ) : (
                  lane.map((order) => (
                    <TicketCard
                      key={order.id}
                      order={order}
                      onPayment={createPayment}
                      onShipment={createShipment}
                    />
                  ))
                )}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
