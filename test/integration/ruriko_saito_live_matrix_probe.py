#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import sys
import urllib.parse
import urllib.request


def _auth_headers(token: str) -> dict[str, str]:
    return {
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
    }


def _load_payload_arg_or_stdin(payload: str) -> dict:
    raw = payload.strip() if payload is not None else ""
    if not raw:
        raw = sys.stdin.read().strip()
    if not raw:
        return {}
    try:
        obj = json.loads(raw)
        if isinstance(obj, dict):
            return obj
    except Exception:  # noqa: BLE001
        pass
    return {}


def command_send(args: argparse.Namespace) -> int:
    room_enc = urllib.parse.quote(args.room, safe="")
    txn_enc = urllib.parse.quote(args.txn_id, safe="")
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/rooms/{room_enc}/send/m.room.message/{txn_enc}"

    body = json.dumps({"msgtype": "m.text", "body": args.body}).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={**_auth_headers(args.token), "Content-Type": "application/json"},
        method="PUT",
    )

    try:
        with urllib.request.urlopen(req, timeout=args.timeout) as resp:
            payload = json.loads((resp.read() or b"{}").decode("utf-8", errors="replace"))
    except Exception as err:  # noqa: BLE001
        print(f"send failed: {err}", file=sys.stderr)
        return 1

    event_id = str(payload.get("event_id") or "").strip()
    if not event_id:
        print("send failed: missing event_id", file=sys.stderr)
        return 1

    print(event_id)
    return 0


def command_sync(args: argparse.Namespace) -> int:
    rooms_filter = [x.strip() for x in args.rooms.split(",") if x.strip()]
    senders_filter = [x.strip() for x in args.senders.split(",") if x.strip()]

    filter_obj: dict[str, object] = {}
    room_obj: dict[str, object] = {}
    timeline_obj: dict[str, object] = {"types": ["m.room.message"]}
    if senders_filter:
        timeline_obj["senders"] = senders_filter
    room_obj["timeline"] = timeline_obj
    if rooms_filter:
        room_obj["rooms"] = rooms_filter
    if room_obj:
        filter_obj["room"] = room_obj

    params = {"timeout": str(args.timeout_ms), "filter": json.dumps(filter_obj or {})}
    if args.since.strip():
        params["since"] = args.since.strip()

    query = urllib.parse.urlencode(params)
    url = f"{args.base_url.rstrip('/')}/_matrix/client/v3/sync?{query}"

    req = urllib.request.Request(url, headers=_auth_headers(args.token), method="GET")
    try:
        with urllib.request.urlopen(req, timeout=args.timeout) as resp:
            payload = json.loads((resp.read() or b"{}").decode("utf-8", errors="replace"))
    except Exception as err:  # noqa: BLE001
        print(f"sync failed: {err}", file=sys.stderr)
        return 1

    next_batch = str(payload.get("next_batch") or "").strip()

    rooms_filter_set = set(rooms_filter)
    senders_filter_set = set(senders_filter)

    out_events: list[dict[str, object]] = []
    joined = ((payload.get("rooms") or {}).get("join") or {})
    for room_id, room_payload in joined.items():
        if rooms_filter_set and room_id not in rooms_filter_set:
            continue
        timeline = ((room_payload or {}).get("timeline") or {}).get("events") or []
        for evt in timeline:
            if not isinstance(evt, dict):
                continue
            if str(evt.get("type") or "") != "m.room.message":
                continue

            sender = str(evt.get("sender") or "").strip()
            if senders_filter_set and sender not in senders_filter_set:
                continue

            content = evt.get("content") or {}
            if not isinstance(content, dict):
                continue
            if str(content.get("msgtype") or "") != "m.text":
                continue

            out_events.append(
                {
                    "room_id": room_id,
                    "sender": sender,
                    "event_id": str(evt.get("event_id") or "").strip(),
                    "body": str(content.get("body") or ""),
                    "origin_server_ts": int(evt.get("origin_server_ts") or 0),
                }
            )

    print(json.dumps({"next_batch": next_batch, "events": out_events}, ensure_ascii=False))
    return 0


def command_events_print(args: argparse.Namespace) -> int:
    payload = _load_payload_arg_or_stdin(args.payload)
    events = payload.get("events") or []
    if not isinstance(events, list):
        return 0

    for item in events:
        if not isinstance(item, dict):
            continue
        ts_ms = int(item.get("origin_server_ts") or 0)
        if ts_ms > 0:
            ts = datetime.datetime.fromtimestamp(ts_ms / 1000, tz=datetime.timezone.utc).isoformat()
        else:
            ts = "unknown"
        room_id = str(item.get("room_id") or "")
        sender = str(item.get("sender") or "")
        body = str(item.get("body") or "")
        print(f"[MATRIX][recv] ts={ts} room={room_id} sender={sender} body={body}")
    return 0


def command_events_count(args: argparse.Namespace) -> int:
    payload = _load_payload_arg_or_stdin(args.payload)
    events = payload.get("events") or []
    if not isinstance(events, list):
        print("0")
        return 0

    count = 0
    for item in events:
        if not isinstance(item, dict):
            continue
        if args.room and str(item.get("room_id") or "") != args.room:
            continue
        if args.sender and str(item.get("sender") or "") != args.sender:
            continue
        if args.contains and args.contains not in str(item.get("body") or ""):
            continue
        count += 1

    print(str(count))
    return 0


def command_next_batch(args: argparse.Namespace) -> int:
    payload = _load_payload_arg_or_stdin(args.payload)
    print(str(payload.get("next_batch") or ""))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Matrix probing helpers for deterministic live integration tests")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("send")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--room", required=True)
    p.add_argument("--body", required=True)
    p.add_argument("--txn-id", required=True)
    p.add_argument("--timeout", type=int, default=12)
    p.set_defaults(func=command_send)

    p = sub.add_parser("sync")
    p.add_argument("--base-url", required=True)
    p.add_argument("--token", required=True)
    p.add_argument("--since", default="")
    p.add_argument("--timeout-ms", type=int, default=30000)
    p.add_argument("--timeout", type=int, default=40)
    p.add_argument("--rooms", default="")
    p.add_argument("--senders", default="")
    p.set_defaults(func=command_sync)

    p = sub.add_parser("events-print")
    p.add_argument("--payload", default="")
    p.set_defaults(func=command_events_print)

    p = sub.add_parser("events-count")
    p.add_argument("--payload", default="")
    p.add_argument("--room", default="")
    p.add_argument("--sender", default="")
    p.add_argument("--contains", default="")
    p.set_defaults(func=command_events_count)

    p = sub.add_parser("next-batch")
    p.add_argument("--payload", default="")
    p.set_defaults(func=command_next_batch)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
