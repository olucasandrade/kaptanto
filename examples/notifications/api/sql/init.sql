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

INSERT INTO users (id, name) VALUES
  ('ava', 'Ava'),
  ('jules', 'Jules'),
  ('morgan', 'Morgan');

