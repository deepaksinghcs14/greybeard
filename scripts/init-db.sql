-- greybeard database setup.
--
-- Prerequisites: a local PostgreSQL (13+) with the Apache AGE extension
-- installed. Easiest path is the official image:
--
--   docker run -d --name greybeard-db -p 5432:5432 \
--     -e POSTGRES_PASSWORD=greybeard apache/age
--
-- Then create the database and run this file against it:
--
--   createdb -h localhost -U postgres greybeard
--   psql -h localhost -U postgres -d greybeard -f scripts/init-db.sql
--
-- Point greybeard at it with GREYBEARD_DB_URL, e.g.
--   export GREYBEARD_DB_URL='postgres://postgres:greybeard@localhost:5432/greybeard?sslmode=disable'
--
-- Running this file is optional: `greybeard init/build/serve` performs the
-- same bootstrap on startup. It exists so you can set up (or inspect) the
-- store by hand.

CREATE EXTENSION IF NOT EXISTS age;
LOAD 'age';
SET search_path = ag_catalog, "$user", public;

-- create_graph errors if the graph already exists, so guard it.
DO $bootstrap$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name = 'greybeard') THEN
    PERFORM ag_catalog.create_graph('greybeard');
  END IF;
END
$bootstrap$;
