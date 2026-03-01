#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import socket
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


def command_db_has_agents(args: argparse.Namespace) -> int:
    wanted = [x.strip().lower() for x in (args.ids or "").split(",") if x.strip()]
    if not wanted:
        return 1

    conn = sqlite3.connect(args.db_file)
    try:
        cur = conn.cursor()
        placeholders = ",".join("?" for _ in wanted)
        cur.execute(
            f"select lower(id) from agents where lower(id) in ({placeholders})",
            wanted,
        )
        found = {str(row[0]).lower() for row in cur.fetchall()}
    finally:
        conn.close()

    missing = [name for name in wanted if name not in found]
    if missing:
        print(",".join(missing))
        return 1

    print("")
    return 0


def command_db_upsert_agent_from_container(args: argparse.Namespace) -> int:
    inspect = subprocess.run(["docker", "inspect", args.container], capture_output=True, text=True)
    if inspect.returncode != 0:
        print(f"docker inspect failed for {args.container}: {inspect.stderr.strip()}", file=sys.stderr)
        return 1

    try:
        payload = json.loads(inspect.stdout)
        if not isinstance(payload, list) or not payload:
            raise ValueError("empty inspect payload")
        meta = payload[0]
    except Exception as err:  # noqa: BLE001
        print(f"invalid inspect payload for {args.container}: {err}", file=sys.stderr)
        return 1

    env_map: dict[str, str] = {}
    for item in (meta.get("Config") or {}).get("Env") or []:
        if "=" not in str(item):
            continue
        key, value = str(item).split("=", 1)
        env_map[key] = value

    networks = ((meta.get("NetworkSettings") or {}).get("Networks") or {}).values()
    ip_addr = ""
    for net in networks:
        ip_addr = str((net or {}).get("IPAddress") or "").strip()
        if ip_addr:
            break

    if not ip_addr:
        print(f"container {args.container} has no IP address", file=sys.stderr)
        return 1

    control_url = f"http://{ip_addr}:8765"
    acp_token = (env_map.get("GITAI_ACP_TOKEN") or env_map.get("ACP_TOKEN") or "").strip()
    if not acp_token:
        print(f"container {args.container} missing GITAI_ACP_TOKEN", file=sys.stderr)
        return 1

    now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat()
    status = "running" if bool((meta.get("State") or {}).get("Running")) else "stopped"

    conn = sqlite3.connect(args.db_file)
    try:
        cur = conn.cursor()
        cur.execute("PRAGMA table_info(agents)")
        cols = {str(row[1]) for row in cur.fetchall()}

        values: dict[str, object] = {
            "id": args.agent_id,
            "display_name": args.agent_id,
            "template": args.template,
            "status": status,
        }

        optional_values: dict[str, object] = {
            "container_id": str(meta.get("Id") or "")[:12],
            "control_url": control_url,
            "image": str((meta.get("Config") or {}).get("Image") or ""),
            "acp_token": acp_token,
            "provisioning_state": "healthy" if status == "running" else "error",
            "updated_at": now,
            "created_at": now,
            "enabled": 1,
        }

        for key, value in optional_values.items():
            if key in cols and value not in (None, ""):
                values[key] = value

        insert_cols = [c for c in values.keys() if c in cols]
        if "id" not in insert_cols:
            print("agents table is missing required id column", file=sys.stderr)
            return 1

        placeholders = ",".join("?" for _ in insert_cols)
        updates = [c for c in insert_cols if c not in {"id", "created_at"}]
        if updates:
            on_conflict = " ON CONFLICT(id) DO UPDATE SET " + ", ".join([f"{c}=excluded.{c}" for c in updates])
        else:
            on_conflict = " ON CONFLICT(id) DO NOTHING"

        sql = f"INSERT INTO agents ({', '.join(insert_cols)}) VALUES ({placeholders}){on_conflict}"
        params = [values[c] for c in insert_cols]
        cur.execute(sql, params)
        conn.commit()
    finally:
        conn.close()

    print(args.agent_id)
    return 0


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
        parsed = urllib.parse.urlparse(homeserver)
        host = (parsed.hostname or "").strip()
        host_ok = False
        if host:
            try:
                socket.gethostbyname(host)
                host_ok = True
            except Exception:
                host_ok = False
        if host_ok:
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
    try:
        payload = json.load(sys.stdin)
    except Exception:
        return 1
    rooms = payload.get("joined_rooms") or []
    return 0 if args.room in rooms else 1


def command_extract_join_room_id(args: argparse.Namespace) -> int:
    raw = (args.payload or "").strip()
    if not raw:
        print(args.fallback)
        return 0

    try:
        payload = json.loads(raw)
    except Exception:
        print(args.fallback)
        return 0

    room_id = ""
    if isinstance(payload, dict):
        room_id = str(payload.get("room_id") or "").strip()

    print(room_id or args.fallback)
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Helpers for canonical live compose integration tests")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("db-ready")
    p.add_argument("--db-file", required=True)
    p.set_defaults(func=command_db_ready)

    p = sub.add_parser("db-has-agents")
    p.add_argument("--db-file", required=True)
    p.add_argument("--ids", required=True)
    p.set_defaults(func=command_db_has_agents)

    p = sub.add_parser("db-upsert-agent-from-container")
    p.add_argument("--db-file", required=True)
    p.add_argument("--agent-id", required=True)
    p.add_argument("--template", required=True)
    p.add_argument("--container", required=True)
    p.set_defaults(func=command_db_upsert_agent_from_container)

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

    p = sub.add_parser("extract-join-room-id")
    p.add_argument("--payload", required=True)
    p.add_argument("--fallback", required=True)
    p.set_defaults(func=command_extract_join_room_id)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
