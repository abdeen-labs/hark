#!/bin/sh
# dev.sh runs harkd locally against a throwaway PostgreSQL 17 in Docker
# (OrbStack), seeded with scripts/demo-seed.py the first time the database
# comes up. Sign in at http://localhost:8080 as admin / hark-dev-password.
#
# The container keeps its data while it runs, so restarting the server keeps
# the seeded account; `docker rm -f hark-dev-pg` starts over.
set -eu
cd "$(dirname "$0")/.."

docker info >/dev/null 2>&1 || {
	open -a OrbStack
	until docker info >/dev/null 2>&1; do sleep 1; done
}

container=hark-dev-pg
fresh=
if ! docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null | grep -q true; then
	docker rm -f "$container" >/dev/null 2>&1 || true
	docker run -d --name "$container" \
		-e POSTGRES_USER=hark -e POSTGRES_PASSWORD=hark -e POSTGRES_DB=hark \
		-p 54318:5432 postgres:17-alpine >/dev/null
	fresh=1
fi
until docker exec "$container" pg_isready -U hark -d hark >/dev/null 2>&1; do sleep 1; done

export DATABASE_URL='postgres://hark:hark@localhost:54318/hark?sslmode=disable'
export HARK_SECRET_KEY='hark-dev-secret-key-for-a-throwaway-database-only'
export HARK_ADMIN_PASSWORD='hark-dev-password'
export HARK_ENV=development
export HARK_PUBLIC_URL=http://localhost:8080
export HARK_LOG_FORMAT=text

if [ -n "$fresh" ]; then
	(
		until curl -sf localhost:8080/healthz >/dev/null; do sleep 0.5; done
		python3 scripts/demo-seed.py
		python3 scripts/demo-seed.py --sql | docker exec -i "$container" psql -U hark -d hark -q
	) &
fi

exec go run ./cmd/harkd
