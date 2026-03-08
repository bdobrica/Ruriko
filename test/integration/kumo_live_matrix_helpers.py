#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
import urllib.parse
import urllib.request


def _request_json(method: str, url: str, *, token: str = "", payload: dict | None = None, timeout: int = 20) -> dict:
    data = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if token.strip():
        headers["Authorization"] = f"Bearer {token.strip()}"

    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read() or b"{}"
        return json.loads(raw.decode("utf-8", errors="replace"))


def command_register(args: argparse.Namespace) -> int:
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/register"
    payload = {
        "username": args.username,
        "password": args.password,
        "auth": {
            "type": "m.login.registration_token",
            "token": args.registration_token,
        },
    }

    try:
        obj = _request_json("POST", url, payload=payload, timeout=args.timeout)
    except Exception as err:  # noqa: BLE001
        print(f"register failed: {err}", file=sys.stderr)
        return 1

    access_token = str(obj.get("access_token") or "").strip()
    user_id = str(obj.get("user_id") or "").strip()
    if not access_token or not user_id:
        print(f"register failed: unexpected response {json.dumps(obj)}", file=sys.stderr)
        return 1

    print(json.dumps({"access_token": access_token, "user_id": user_id}, ensure_ascii=True))
    return 0


def command_create_room(args: argparse.Namespace) -> int:
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/createRoom"
    payload: dict[str, object] = {
        "name": args.name,
        "preset": "private_chat",
        "is_direct": bool(args.is_direct),
    }
    if args.invite.strip():
        payload["invite"] = [x.strip() for x in args.invite.split(",") if x.strip()]

    try:
        obj = _request_json("POST", url, token=args.token, payload=payload, timeout=args.timeout)
    except Exception as err:  # noqa: BLE001
        print(f"create room failed: {err}", file=sys.stderr)
        return 1

    room_id = str(obj.get("room_id") or "").strip()
    if not room_id:
        print(f"create room failed: unexpected response {json.dumps(obj)}", file=sys.stderr)
        return 1
    print(room_id)
    return 0


def command_invite(args: argparse.Namespace) -> int:
    room_enc = urllib.parse.quote(args.room_id, safe="")
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/rooms/{room_enc}/invite"
    payload = {"user_id": args.user_id}

    try:
        _request_json("POST", url, token=args.token, payload=payload, timeout=args.timeout)
    except Exception as err:  # noqa: BLE001
        print(f"invite failed: {err}", file=sys.stderr)
        return 1
    print("ok")
    return 0


def command_join(args: argparse.Namespace) -> int:
    room_enc = urllib.parse.quote(args.room_id, safe="")
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/rooms/{room_enc}/join"

    try:
        obj = _request_json("POST", url, token=args.token, payload={}, timeout=args.timeout)
    except Exception as err:  # noqa: BLE001
        print(f"join failed: {err}", file=sys.stderr)
        return 1

    room_id = str(obj.get("room_id") or "").strip()
    if not room_id:
        print(f"join failed: unexpected response {json.dumps(obj)}", file=sys.stderr)
        return 1
    print(room_id)
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Matrix helper commands for Kumo live integration tests")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("register")
    p.add_argument("--base-url", required=True)
    p.add_argument("--username", required=True)
    p.add_argument("--password", required=True)
    p.add_argument("--registration-token", required=True)
    p.add_argument("--timeout", type=int, default=20)
    p.set_defaults(func=command_register)

    p = sub.add_parser("create-room")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--name", required=True)
    p.add_argument("--invite", default="")
    p.add_argument("--is-direct", action="store_true")
    p.add_argument("--timeout", type=int, default=20)
    p.set_defaults(func=command_create_room)

    p = sub.add_parser("invite")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--room-id", required=True)
    p.add_argument("--user-id", required=True)
    p.add_argument("--timeout", type=int, default=20)
    p.set_defaults(func=command_invite)

    p = sub.add_parser("join")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--room-id", required=True)
    p.add_argument("--timeout", type=int, default=20)
    p.set_defaults(func=command_join)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
