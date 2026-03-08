#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.parse
import urllib.request


def _auth_headers(token: str) -> dict[str, str]:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
    }


def _sync_events(base_url: str, token: str, since: str, timeout_ms: int, timeout_seconds: int) -> tuple[str, list[dict[str, object]]]:
    params = {"timeout": str(timeout_ms), "filter": "{}"}
    if since.strip():
        params["since"] = since.strip()

    query = urllib.parse.urlencode(params)
    url = f"{base_url.rstrip('/')}/_matrix/client/v3/sync?{query}"

    req = urllib.request.Request(url, headers=_auth_headers(token), method="GET")
    with urllib.request.urlopen(req, timeout=timeout_seconds) as resp:
        payload = json.loads((resp.read() or b"{}").decode("utf-8", errors="replace"))

    next_batch = str(payload.get("next_batch") or "").strip()
    events: list[dict[str, object]] = []

    joined = ((payload.get("rooms") or {}).get("join") or {})
    for room_id, room_payload in joined.items():
        timeline = ((room_payload or {}).get("timeline") or {}).get("events") or []
        for evt in timeline:
            if not isinstance(evt, dict):
                continue
            if str(evt.get("type") or "") != "m.room.message":
                continue
            content = evt.get("content") or {}
            if not isinstance(content, dict):
                continue
            if str(content.get("msgtype") or "") != "m.text":
                continue
            events.append(
                {
                    "room_id": room_id,
                    "sender": str(evt.get("sender") or "").strip(),
                    "event_id": str(evt.get("event_id") or "").strip(),
                    "body": str(content.get("body") or ""),
                    "origin_server_ts": int(evt.get("origin_server_ts") or 0),
                }
            )

    return next_batch, events


def command_wait_message(args: argparse.Namespace) -> int:
    deadline = time.time() + args.wait_seconds
    since = ""

    while time.time() < deadline:
        try:
            next_batch, events = _sync_events(
                args.base_url,
                args.token,
                since,
                args.sync_timeout_ms,
                args.http_timeout,
            )
        except Exception as err:  # noqa: BLE001
            print(f"sync failed: {err}", file=sys.stderr)
            time.sleep(args.poll_seconds)
            continue

        if next_batch:
            since = next_batch

        for item in events:
            if args.room and str(item.get("room_id") or "") != args.room:
                continue
            if args.sender and str(item.get("sender") or "") != args.sender:
                continue
            body = str(item.get("body") or "")
            if args.contains and args.contains not in body:
                continue
            print(json.dumps(item, ensure_ascii=False))
            return 0

        time.sleep(args.poll_seconds)

    print("message not observed before timeout", file=sys.stderr)
    return 1


def command_wait_count(args: argparse.Namespace) -> int:
    deadline = time.time() + args.wait_seconds
    since = ""
    seen_event_ids: set[str] = set()
    matched: list[dict[str, object]] = []

    while time.time() < deadline:
        try:
            next_batch, events = _sync_events(
                args.base_url,
                args.token,
                since,
                args.sync_timeout_ms,
                args.http_timeout,
            )
        except Exception as err:  # noqa: BLE001
            print(f"sync failed: {err}", file=sys.stderr)
            time.sleep(args.poll_seconds)
            continue

        if next_batch:
            since = next_batch

        for item in events:
            event_id = str(item.get("event_id") or "").strip()
            if not event_id or event_id in seen_event_ids:
                continue
            if args.room and str(item.get("room_id") or "") != args.room:
                continue
            if args.sender and str(item.get("sender") or "") != args.sender:
                continue
            body = str(item.get("body") or "")
            if args.contains and args.contains not in body:
                continue
            seen_event_ids.add(event_id)
            matched.append(item)

        if len(matched) >= args.count:
            print(json.dumps({"count": len(matched), "events": matched}, ensure_ascii=False))
            return 0

        time.sleep(args.poll_seconds)

    print(
        f"observed {len(matched)} matching messages before timeout; required {args.count}",
        file=sys.stderr,
    )
    return 1


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Matrix verification helpers for standalone Saito live tests")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("wait-message")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--room", default="")
    p.add_argument("--sender", default="")
    p.add_argument("--contains", default="")
    p.add_argument("--wait-seconds", type=int, default=180)
    p.add_argument("--poll-seconds", type=int, default=4)
    p.add_argument("--sync-timeout-ms", type=int, default=10000)
    p.add_argument("--http-timeout", type=int, default=20)
    p.set_defaults(func=command_wait_message)

    p = sub.add_parser("wait-count")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--room", default="")
    p.add_argument("--sender", default="")
    p.add_argument("--contains", default="")
    p.add_argument("--count", type=int, required=True)
    p.add_argument("--wait-seconds", type=int, default=180)
    p.add_argument("--poll-seconds", type=int, default=4)
    p.add_argument("--sync-timeout-ms", type=int, default=10000)
    p.add_argument("--http-timeout", type=int, default=20)
    p.set_defaults(func=command_wait_count)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
