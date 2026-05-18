CREATE TABLE users (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL
);

CREATE TABLE comments (
  id TEXT PRIMARY KEY,
  author_id TEXT NOT NULL REFERENCES users(id),
  post_id TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE mentions (
  id TEXT PRIMARY KEY,
  comment_id TEXT NOT NULL REFERENCES comments(id),
  actor_id TEXT NOT NULL REFERENCES users(id),
  target_user_id TEXT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE follows (
  id TEXT PRIMARY KEY,
  follower_id TEXT NOT NULL REFERENCES users(id),
  target_user_id TEXT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE users REPLICA IDENTITY FULL;
ALTER TABLE comments REPLICA IDENTITY FULL;
ALTER TABLE mentions REPLICA IDENTITY FULL;
ALTER TABLE follows REPLICA IDENTITY FULL;

-- ── Users ──
INSERT INTO users (id, name) VALUES
  ('ava',    'Ava Martin'),
  ('jules',  'Jules Chen'),
  ('morgan', 'Morgan Lee'),
  ('priya',  'Priya Patel'),
  ('sam',    'Sam Torres');

-- ── Seed comments ──
INSERT INTO comments (id, author_id, post_id, body) VALUES
  ('cmt-01', 'ava',    'roadmap-q2',  'Can you review the rollout plan before the sprint kicks off?'),
  ('cmt-02', 'morgan', 'incident-14', 'The latency spike correlates with the 03:12 deploy window.'),
  ('cmt-03', 'jules',  'roadmap-q2',  'Left some inline notes — flag me if the scope changes.'),
  ('cmt-04', 'priya',  'design-v3',   'Component library is ready for review, check the Figma link.'),
  ('cmt-05', 'sam',    'infra-22',    'Redis cluster memory is at 78% — should we bump the instance?'),
  ('cmt-06', 'ava',    'design-v3',   'The new button states look great, only the hover needs tweaking.'),
  ('cmt-07', 'morgan', 'incident-14', 'Confirmed — rolling back the migration fixed it.');

-- ── Seed mentions ── (each generates a notification via CDC backfill)
INSERT INTO mentions (id, comment_id, actor_id, target_user_id) VALUES
  ('men-01', 'cmt-01', 'ava',    'jules'),
  ('men-02', 'cmt-02', 'morgan', 'priya'),
  ('men-03', 'cmt-03', 'jules',  'ava'),
  ('men-04', 'cmt-04', 'priya',  'morgan'),
  ('men-05', 'cmt-05', 'sam',    'jules'),
  ('men-06', 'cmt-06', 'ava',    'priya'),
  ('men-07', 'cmt-07', 'morgan', 'sam');

-- ── Seed follows ── (each generates a notification via CDC backfill)
INSERT INTO follows (id, follower_id, target_user_id) VALUES
  ('flw-01', 'jules',  'ava'),
  ('flw-02', 'priya',  'jules'),
  ('flw-03', 'sam',    'morgan'),
  ('flw-04', 'morgan', 'priya'),
  ('flw-05', 'ava',    'sam');
