import { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type Employee = {
  id: string; name: string; email: string;
  department: string; title: string; salaryCents: number;
};

type FieldChange = { field: string; from: string; to: string };
type AuditEntry = {
  id: string; operation: string; employeeId: string; employeeName: string;
  changes: FieldChange[]; timestamp: string;
};

type Snapshot = { employees: Employee[]; auditLog: AuditEntry[] };

const apiUrl = import.meta.env.VITE_API_URL ?? "http://localhost:4007";

const DEPARTMENTS = ["Engineering", "Product", "Design", "Marketing", "Operations", "Sales"];
const TITLES: Record<string, string[]> = {
  Engineering: ["Engineer", "Senior Engineer", "Staff Engineer", "Principal Engineer", "Engineering Manager"],
  Product: ["Associate PM", "Product Manager", "Senior PM", "Director of Product"],
  Design: ["Designer", "Senior Designer", "Lead Designer", "Design Manager"],
  Marketing: ["Marketing Coordinator", "Marketing Manager", "Senior Marketing Manager"],
  Operations: ["Operations Analyst", "Operations Manager", "Director of Operations"],
  Sales: ["Sales Rep", "Account Executive", "Senior AE", "Sales Manager"],
};

function fmt(cents: number) {
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 }).format(cents / 100);
}

function fieldLabel(f: string) {
  const map: Record<string, string> = { salary_cents: "salary", name: "name", email: "email", department: "dept", title: "title" };
  return map[f] ?? f;
}

function OperationBadge({ op }: { op: string }) {
  const cls = { insert: "badge-insert", update: "badge-update", delete: "badge-delete" }[op] ?? "badge-update";
  return <span className={`op-badge ${cls}`}>{op}</span>;
}

function EmployeeCard({ emp, onEdit }: {
  emp: Employee;
  onEdit: (id: string, field: string, value: string | number) => void;
}) {
  const initials = emp.name.split(" ").map((p) => p[0]).join("").toUpperCase().slice(0, 2);

  async function raiseSalary() { onEdit(emp.id, "salaryCents", Math.round(emp.salaryCents * 1.1)); }
  async function transferDept() {
    const next = DEPARTMENTS[(DEPARTMENTS.indexOf(emp.department) + 1) % DEPARTMENTS.length];
    const title = (TITLES[next] ?? TITLES.Engineering!)[0];
    onEdit(emp.id, "department", next);
    // title will follow as a separate update for clarity
    setTimeout(() => onEdit(emp.id, "title", title), 200);
  }
  async function promoteTitle() {
    const titles = TITLES[emp.department] ?? TITLES.Engineering!;
    const idx = titles.indexOf(emp.title);
    const nextTitle = titles[Math.min(idx + 1, titles.length - 1)];
    onEdit(emp.id, "title", nextTitle);
  }

  return (
    <div className="emp-card">
      <div className="emp-top">
        <div className="emp-avatar">{initials}</div>
        <div className="emp-info">
          <div className="emp-name">{emp.name}</div>
          <div className="emp-email">{emp.email}</div>
        </div>
        <div className="emp-salary">{fmt(emp.salaryCents)}</div>
      </div>
      <div className="emp-meta">
        <span className="dept-chip">{emp.department}</span>
        <span className="title-chip">{emp.title}</span>
      </div>
      <div className="emp-actions">
        <button className="btn-sm" onClick={raiseSalary}>+10% salary</button>
        <button className="btn-sm" onClick={promoteTitle}>Promote</button>
        <button className="btn-sm" onClick={transferDept}>Transfer dept</button>
      </div>
    </div>
  );
}

function AuditRow({ entry }: { entry: AuditEntry }) {
  const time = new Date(entry.timestamp).toLocaleTimeString();
  return (
    <div className="audit-row">
      <div className="audit-meta">
        <OperationBadge op={entry.operation} />
        <span className="audit-name">{entry.employeeName}</span>
        <span className="audit-time">{time}</span>
      </div>
      {entry.changes.length > 0 && (
        <div className="audit-changes">
          {entry.changes.map((c, i) => (
            <div key={i} className="change-row">
              <span className="change-field">{fieldLabel(c.field)}</span>
              <span className="change-from">{c.from}</span>
              <span className="change-arrow">→</span>
              <span className="change-to">{c.to}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function App() {
  const [data, setData] = useState<Snapshot>({ employees: [], auditLog: [] });
  const [newName, setNewName] = useState("Casey Park");
  const [newDept, setNewDept] = useState("Engineering");

  useEffect(() => {
    fetch(`${apiUrl}/api/bootstrap`).then((r) => r.json()).then(setData);
    const es = new EventSource(`${apiUrl}/api/events`);
    es.onmessage = (e) => setData(JSON.parse(e.data));
    return () => es.close();
  }, []);

  async function editEmployee(id: string, field: string, value: string | number) {
    await fetch(`${apiUrl}/api/employees/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ [field]: value }),
    });
  }

  async function addEmployee() {
    const titles = TITLES[newDept] ?? TITLES.Engineering!;
    await fetch(`${apiUrl}/api/employees`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: newName, department: newDept, title: titles[0], salaryCents: 10000000 }),
    });
  }

  return (
    <div className="page">
      <header className="header">
        <div className="header-kicker">
          <span className="kicker-dot" />
          kaptanto example 02
        </div>
        <div className="brand-lockup">
          <div className="brand-mark">k</div>
          <div>
            <div className="brand-name">kaptanto</div>
            <div className="brand-sub">recent changes everywhere</div>
          </div>
        </div>
        <h1>Turn raw row updates into a user-facing activity feed.</h1>
        <p className="header-desc">
          Every UPDATE arrives with full before and after state. The consumer diffs the fields and builds
          a timeline people can actually read: promotions, transfers, salary changes, and new records,
          all derived from CDC instead of a second write path.
        </p>
      </header>

      <div className="layout">
        {/* Left: employees + add */}
        <div className="left-col">
          <div className="panel">
            <div className="panel-header">
              <span className="panel-title">Add employee</span>
            </div>
            <div className="panel-body">
              <div className="form-group">
                <label className="form-label">Name</label>
                <input value={newName} onChange={(e) => setNewName(e.target.value)} />
              </div>
              <div className="form-group">
                <label className="form-label">Department</label>
                <select value={newDept} onChange={(e) => setNewDept(e.target.value)}>
                  {DEPARTMENTS.map((d) => <option key={d}>{d}</option>)}
                </select>
              </div>
              <button className="btn-primary" onClick={addEmployee}>Hire employee</button>
            </div>
          </div>

          <div className="employees-section">
            {data.employees.map((emp) => (
              <EmployeeCard key={emp.id} emp={emp} onEdit={editEmployee} />
            ))}
          </div>
        </div>

        {/* Right: audit timeline */}
        <div className="right-col">
          <div className="panel audit-panel">
            <div className="panel-header">
              <span className="panel-title">Audit timeline</span>
              <span className="panel-count">{data.auditLog.length} changes</span>
            </div>
            <div className="audit-list">
              {data.auditLog.length === 0 ? (
                <div className="audit-empty">No changes yet. Edit an employee record and the activity feed will populate in real time.</div>
              ) : (
                data.auditLog.map((entry) => <AuditRow key={entry.id} entry={entry} />)
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
