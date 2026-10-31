-- Sign-in with Active Directory / LDAP and single sign-on (1.1): where an
-- account comes from. Directory accounts have no password of their own.
ALTER TABLE users ADD COLUMN source text NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'ldap', 'sso'));
ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT '';
