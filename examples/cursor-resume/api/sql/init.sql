CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  priority TEXT NOT NULL DEFAULT 'normal',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE jobs REPLICA IDENTITY FULL;

INSERT INTO jobs (id, title, status, priority) VALUES
  ('job-seed-1', 'Build search index',       'completed', 'high'),
  ('job-seed-2', 'Send welcome emails',       'running',   'normal'),
  ('job-seed-3', 'Generate monthly report',   'queued',    'low'),
  ('job-seed-4', 'Sync user profiles',        'queued',    'normal'),
  ('job-seed-5', 'Archive old records',       'queued',    'low'),
  ('job-seed-6', 'Reindex product catalog',   'completed', 'high'),
  ('job-seed-7', 'Prune expired sessions',    'running',   'normal');
