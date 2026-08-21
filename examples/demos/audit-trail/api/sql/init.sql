CREATE TABLE employees (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  email TEXT NOT NULL,
  department TEXT NOT NULL,
  title TEXT NOT NULL,
  salary_cents INT NOT NULL,
  hired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE employees REPLICA IDENTITY FULL;

INSERT INTO employees (id, name, email, department, title, salary_cents) VALUES
  ('emp-1', 'Alex Chen',       'alex@example.com',    'Engineering', 'Senior Engineer',   14500000),
  ('emp-2', 'Jamie Rodriguez', 'jamie@example.com',   'Product',     'Product Manager',   13000000),
  ('emp-3', 'Sam Williams',    'sam@example.com',     'Design',      'Lead Designer',     12000000),
  ('emp-4', 'Morgan Lee',      'morgan@example.com',  'Engineering', 'Engineer',          11500000),
  ('emp-5', 'Riley Kim',       'riley@example.com',   'Marketing',   'Marketing Manager', 11000000);
