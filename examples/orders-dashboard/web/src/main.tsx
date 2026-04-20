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

function App() {
  const [data, setData] = useState<Snapshot>({ orders: [], recentEvents: [] });
  const [customerName, setCustomerName] = useState("Beacon Commerce");
  const [totalCents, setTotalCents] = useState(14900);

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`)
      .then((response) => response.json())
      .then(setData);

    const stream = new EventSource(`${apiUrl}/api/events`);
    stream.onmessage = (event) => setData(JSON.parse(event.data));
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
    await fetch(`${apiUrl}/api/payments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ orderId, amountCents: totalCents, status: "captured" }),
    });
  }

  async function createShipment(orderId: string) {
    await fetch(`${apiUrl}/api/shipments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        orderId,
        carrier: "DHL",
        status: data.orders.find((order) => order.id === orderId)?.shipmentStatus === "packed" ? "delivered" : "packed",
      }),
    });
  }

  const lanes = {
    created: data.orders.filter((order) => order.shipmentStatus === "not_created"),
    processing: data.orders.filter((order) => order.shipmentStatus === "packed"),
    complete: data.orders.filter((order) => order.orderStatus === "completed"),
  };

  return (
    <div className="page">
      <section className="hero">
        <h1>Live Order Operations</h1>
        <p>CDC keeps the operational view in sync as orders, payments, and shipments evolve.</p>
      </section>

      <div className="layout">
        <section className="card grid">
          <h2>Trigger order flow</h2>
          <label>
            Customer
            <input value={customerName} onChange={(event) => setCustomerName(event.target.value)} />
          </label>
          <label>
            Total cents
            <input
              value={totalCents}
              onChange={(event) => setTotalCents(Number(event.target.value))}
              type="number"
            />
          </label>
          <button onClick={createOrder}>Create order</button>
          <h3>Recent CDC-driven updates</h3>
          <div className="grid">
            {data.recentEvents.map((event) => (
              <div key={event}>{event}</div>
            ))}
          </div>
        </section>

        <section className="board">
          {([
            { title: "New orders", lane: lanes.created },
            { title: "Packing", lane: lanes.processing },
            { title: "Complete", lane: lanes.complete },
          ]).map(({ title, lane }) => (
            <div className="lane card" key={title}>
              <h2>{title}</h2>
              {lane.map((order) => (
                <div className="ticket" key={order.id}>
                  <strong>{order.customerName}</strong>
                  <div>{order.id.slice(0, 8)}</div>
                  <div>Order: {order.orderStatus}</div>
                  <div>Payment: {order.paymentStatus}</div>
                  <div>Shipment: {order.shipmentStatus}</div>
                  <div style={{ display: "flex", gap: 8, marginTop: 10 }}>
                    <button onClick={() => createPayment(order.id)}>Capture payment</button>
                    <button onClick={() => createShipment(order.id)}>
                      {order.shipmentStatus === "packed" ? "Mark delivered" : "Create shipment"}
                    </button>
                  </div>
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
