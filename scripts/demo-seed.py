#!/usr/bin/env python3
"""Seed a running harkd with demo data, through its own API.

Development only: it registers fake devices, creates real credentials, and
writes real history to the server's configured database. Never point it at a
deployment you care about.

    HARK_ADMIN_PASSWORD=… python3 scripts/demo-seed.py [--url http://localhost:8080]

Without APNs credentials the server records every delivery as failed, producing
a uniform dashboard. `--sql` prints the statements
that rewrite a handful of the seeded rows into accepted, partial and running
states; pipe them into psql against the same database:

    python3 scripts/demo-seed.py --sql | psql "$DATABASE_URL"
"""

import argparse
import json
import os
import secrets
import sys
import urllib.error
import urllib.request

STATUS_SQL = """
UPDATE events SET status = 'accepted', delivered_count = 2, error = NULL
 WHERE title IN ('Build 4821 succeeded', 'Deploy live', 'Nightly run complete', 'hark.abdeen.dev is back');
UPDATE events SET status = 'partial', delivered_count = 1, error = 'Unregistered: one device'
 WHERE title = 'hark.abdeen.dev is down';
UPDATE events e SET created_at = now() - s.n * interval '47 minutes'
  FROM (SELECT id, row_number() OVER (ORDER BY created_at DESC) AS n FROM events) s
 WHERE e.id = s.id;
UPDATE agent_notifications SET status = 'accepted', accepted_count = 2 WHERE body LIKE 'Migration%';
UPDATE interactions SET status = 'approved', response = 'approve', responded_at = now() - interval '12 minutes'
 WHERE correlation_id = 'deploy-4821';
UPDATE live_activities SET status = 'active', accepted_count = 1, failed_count = 0, sequence = 3,
       updated_at = now() - interval '2 minutes';
UPDATE api_tokens SET last_used_at = now() - interval '3 hours' WHERE name = 'deploy-bot';
UPDATE devices SET last_seen_at = now() - interval '6 minutes' WHERE name = 'Studio iPhone';
UPDATE devices SET last_seen_at = now() - interval '3 days', created_at = now() - interval '41 days'
 WHERE name = 'Bench iPad';
"""

SERVICES = [
    ("CI", "https://github.com/actions.png", "https://github.com/abdeen-labs/hark/actions", "normal"),
    ("Railway", "https://github.com/railwayapp.png", "https://railway.app", "time_sensitive"),
    ("Uptime", "https://github.com/louislam.png", None, "time_sensitive"),
]

HOOKS = [
    ("CI", "Build 4821 succeeded", "main · 3m 12s · 214 tests"),
    ("CI", "Build 4822 failed", "feature/redesign · TestHistoryPagesThroughTheArchive"),
    ("Railway", "Deploy live", "hark.abdeen.dev · 302c520 · 41s"),
    ("Uptime", "hark.abdeen.dev is down", "HTTP 502 from 3 regions — escalating"),
    ("Uptime", "hark.abdeen.dev is back", "Downtime 4m 20s"),
    ("CI", "Nightly run complete", "142 passed · 0 failed · 0 skipped"),
]

NOTIFICATIONS = [
    ("Claude Code", "Migration 0007 applied to staging.", "normal"),
    ("Claude Code", "Needs your input on the schema change before it continues.", "time_sensitive"),
]


class Client:
    def __init__(self, base):
        self.base = base.rstrip("/")

    def call(self, method, path, body=None, token=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req) as res:
                raw = res.read()
                return res.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as err:
            raw = err.read()
            return err.code, (json.loads(raw) if raw else {})

    def must(self, method, path, body=None, token=None):
        status, res = self.call(method, path, body, token)
        if status >= 300:
            sys.exit(f"{method} {path}: {status} {json.dumps(res)}")
        return res


def seed(api, username, password):
    session = api.must("POST", "/auth/login", {"username": username, "password": password})["token"]

    devices = []
    for name in ("Studio iPhone", "Bench iPad"):
        device = api.must("POST", "/devices", {
            "apns_token": secrets.token_hex(32), "name": name,
            "interaction_schema_version": 1, "live_activity_interaction_version": 1,
        }, session)["device"]
        devices.append(device["id"])
        print(f"device    {device['id']}  {name}")
    api.must("PUT", f"/devices/{devices[0]}/push-to-start-token",
             {"token": secrets.token_hex(32), "environment": "sandbox", "schema_version": 1}, session)

    hooks = {}
    for title, image, url, priority in SERVICES:
        body = {"title": title, "image_url": image, "priority": priority}
        if url:
            body["url"] = url
        hooks[title] = api.must("POST", "/services", body, session)["webhook_url"]
        print(f"service   {title}")

    bot = api.must("POST", "/tokens", {
        "name": "deploy-bot",
        "scopes": ["notifications:send", "activities:write", "activities:read",
                   "interactions:create", "interactions:read"],
        "expires_in_seconds": 7776000,
    }, session)
    secret = bot.get("secret") or next(v for v in bot.values() if isinstance(v, str) and v.startswith("hark_"))
    api.must("POST", "/tokens", {"name": "ci-reader", "scopes": ["events:read", "devices:read"]}, session)
    stale = api.must("POST", "/tokens", {
        "name": "old-laptop", "scopes": ["notifications:send"], "expires_in_seconds": 3600,
    }, session)["token"]["id"]
    api.must("DELETE", f"/tokens/{stale}", None, session)
    print("tokens    deploy-bot, ci-reader, old-laptop (revoked)")

    for service, title, body in HOOKS:
        path = hooks[service].replace(api.base, "")
        event = api.must("POST", path, {"title": title, "body": body})["event"]
        print(f"hook      {service:8} {title}  → {event['status']}")
    path = hooks["Railway"].replace(api.base, "")
    api.must("POST", path, {
        "title": "Promote to production?", "body": "Build 4821 passed staging. Promote it?",
        "response": {"kind": "approval", "correlation_id": "deploy-4821"},
    })
    print("hook      Railway  Promote to production? (question)")

    for title, body, priority in NOTIFICATIONS:
        api.must("POST", "/notifications", {"title": title, "body": body, "priority": priority}, secret)
        print(f"push      {title}  {body}")
    api.must("POST", "/interactions", {
        "title": "Claude Code", "prompt": "Run the migration on production?",
        "kind": "approval", "expires_in_seconds": 3600,
    }, secret)
    print("question  Claude Code  Run the migration on production?")
    activity = api.must("POST", "/activities", {
        "key": "deploy", "title": "Deploying hark", "status": "Rolling out 3/5",
        "detail": "Railway · us-west", "progress": 0.6, "expires_in_seconds": 7200,
    }, secret)["activity"]
    print(f"activity  deploy  → {activity['status']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("HARK_URL", "http://localhost:8080"))
    parser.add_argument("--sql", action="store_true", help="print the status-mix statements and exit")
    args = parser.parse_args()

    if args.sql:
        print(STATUS_SQL.strip())
        return

    password = os.environ.get("HARK_ADMIN_PASSWORD")
    if not password:
        sys.exit("HARK_ADMIN_PASSWORD is not set")
    seed(Client(args.url), os.environ.get("HARK_ADMIN_USERNAME", "admin"), password)


if __name__ == "__main__":
    main()
