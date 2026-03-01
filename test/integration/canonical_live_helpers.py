#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import shlex
import sqlite3
import subprocess
import sys
import urllib.parse
from pathlib import Path


def parse_env_file(env_file: str) -> dict[str, str]:
    env: dict[str, str] = {}
    for raw in Path(env_file).read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if (value.startswith('"') and value.endswith('"')) or (value.startswith("'") and value.endswith("'")):
            value = value[1:-1]
        env[key.strip()] = value
    return env


def parse_log_line(line: str) -> dict[str, str]:
    s = line.strip()
    if not s:
        return {}

    if s.startswith("{") and s.endswith("}"):
        try:
            obj = json.loads(s)
            if isinstance(obj, dict):
                return {str(k): str(v) for k, v in obj.items()}
        except Exception:
            pass

    out: dict[str, str] = {}
    try:
        for token in shlex.split(s):
            if "=" in token:
                key, val = token.split("=", 1)
                out[key] = val.strip('"')
    except Exception:
        return out
    return out


def command_db_ready(args: argparse.Namespace) -> int:
    conn = sqlite3.connect(args.db_file)
    try:
        cur = conn.cursor()
        cur.execute("select name from sqlite_master where type='table' and name='agents'")
        ok = cur.fetchone() is not None
        return 0 if ok else 1
    finally:
        conn.close()


def command_count_log(args: argparse.Namespace) -> int:
    res = subprocess.run(["docker", "logs", "--since", args.since, args.container], capture_output=True, text=True)
    text = (res.stdout or "") + (res.stderr or "")

    count = 0
    for line in text.splitlines():
        rec = parse_log_line(line)
        if not rec:
            continue
        if args.msg and rec.get("msg") != args.msg:
            continue
        if args.event_type and rec.get("event_type") != args.event_type:
            continue
        if args.agent_id and rec.get("agent_id") != args.agent_id:
            continue
        if args.target and rec.get("target") != args.target:
            continue
        if args.status and rec.get("status") != args.status:
            continue
        count += 1

    print(count)
    return 0


def command_admin_rooms_csv(args: argparse.Namespace) -> int:
    env = parse_env_file(args.env_file)
    rooms = [r.strip() for r in env.get("MATRIX_ADMIN_ROOMS", "").split(",") if r.strip()]
    print(",".join(rooms))
    return 0


def command_admin_room(args: argparse.Namespace) -> int:
    env = parse_env_file(args.env_file)
    rooms = [r.strip() for r in env.get("MATRIX_ADMIN_ROOMS", "").split(",") if r.strip()]
    print(rooms[0] if rooms else "")
    return 0


def command_user_room(args: argparse.Namespace) -> int:
    env = parse_env_file(args.env_file)
    rooms = [r.strip() for r in env.get("MATRIX_ADMIN_ROOMS", "").split(",") if r.strip()]
    if len(rooms) > 1:
        print(rooms[1])
    else:
        print(args.fallback)
    return 0


def command_matrix_base_url(args: argparse.Namespace) -> int:
    env = parse_env_file(args.env_file)
    homeserver = (env.get("MATRIX_HOMESERVER", "").strip() or "").rstrip("/")
    if homeserver:
        print(homeserver)
        return 0

    host = env.get("TUWUNEL_HOST", "127.0.0.1").strip() or "127.0.0.1"
    port = env.get("TUWUNEL_PORT", "8008").strip() or "8008"
    print(f"http://{host}:{port}")
    return 0


def command_urlencode(args: argparse.Namespace) -> int:
    print(urllib.parse.quote(args.value, safe=""))
    return 0


def command_joined_has_room(args: argparse.Namespace) -> int:
    payload = json.load(sys.stdin)
    rooms = payload.get("joined_rooms") or []
    return 0 if args.room in rooms else 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Helpers for canonical live compose integration tests")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("db-ready")
    p.add_argument("--db-file", required=True)
    p.set_defaults(func=command_db_ready)

    p = sub.add_parser("count-log")
    p.add_argument("--container", required=True)
    p.add_argument("--since", required=True)
    p.add_argument("--msg", default="")
    p.add_argument("--event-type", default="")
    p.add_argument("--agent-id", default="")
    p.add_argument("--target", default="")
    p.add_argument("--status", default="success")
    p.set_defaults(func=command_count_log)

    p = sub.add_parser("admin-rooms-csv")
    p.add_argument("--env-file", required=True)
    p.set_defaults(func=command_admin_rooms_csv)

    p = sub.add_parser("admin-room")
    p.add_argument("--env-file", required=True)
    p.set_defaults(func=command_admin_room)

    p = sub.add_parser("user-room")
    p.add_argument("--env-file", required=True)
    p.add_argument("--fallback", default="!canonical-user-fallback:localhost")
    p.set_defaults(func=command_user_room)

    p = sub.add_parser("matrix-base-url")
    p.add_argument("--env-file", required=True)
    p.set_defaults(func=command_matrix_base_url)

    p = sub.add_parser("urlencode")
    p.add_argument("--value", required=True)
    p.set_defaults(func=command_urlencode)

    p = sub.add_parser("joined-has-room")
    p.add_argument("--room", required=True)
    p.set_defaults(func=command_joined_has_room)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
