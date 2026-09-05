-- Existing deployments keep their original account as the administrator.
-- Additional accounts are regular users unless explicitly bootstrapped.
ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'user'
    CHECK (role IN ('admin', 'user'));

UPDATE users SET role = 'admin'
WHERE id = (SELECT id FROM users ORDER BY created_at, id LIMIT 1);

-- Also serializes competing attempts to bootstrap the first administrator.
CREATE UNIQUE INDEX users_one_admin_key ON users (role) WHERE role = 'admin';
