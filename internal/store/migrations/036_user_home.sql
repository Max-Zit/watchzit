-- The page a user starts on: '' = the overview, 'map:<id>' or 'dashboard:<id>'.
ALTER TABLE users ADD COLUMN home text NOT NULL DEFAULT '';
