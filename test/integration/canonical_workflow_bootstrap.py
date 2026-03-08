#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Apply canonical Gosuto configs to saito/kairo/kumo for live tests")
    parser.add_argument("--root-dir", required=True)
    parser.add_argument("--db-file", required=True)
    parser.add_argument("--admin-room", required=True)
    parser.add_argument("--user-room", required=True)
    parser.add_argument("--status-timeout", type=int, default=120)
    parser.add_argument("--apply-timeout", type=int, default=20)
    parser.add_argument("--poll-interval", type=int, default=3)
    parser.add_argument("--saito-cron-every", default="10s")
    return parser.parse_args()


def load_agents(db_file: str) -> dict[str, tuple[str, str]]:
    conn = sqlite3.connect(db_file)
    cur = conn.cursor()
    cur.execute(
        """
        select id, control_url, acp_token
        from agents
        where lower(id) in ('saito','kairo','kumo')
        order by id
        """
    )
    rows = cur.fetchall()
    conn.close()
    return {name.lower(): (control_url or "", token or "") for name, control_url, token in rows}


def render_template(root_dir: str, agent: str, admin_room: str, user_room: str, saito_cron_every: str) -> tuple[str, str]:
    specs = {
        "saito": {
            "template": Path(root_dir) / "templates/saito-agent/gosuto.yaml",
            "subs": {
                "{{.AgentName}}": "saito",
                "{{.AdminRoom}}": admin_room,
                "{{.UserRoom}}": user_room,
                "{{.KairoAdminRoom}}": admin_room,
                "{{.OperatorMXID}}": "@admin:localhost",
            },
        },
        "kairo": {
            "template": Path(root_dir) / "templates/kairo-agent/gosuto.yaml",
            "subs": {
                "{{.AgentName}}": "kairo",
                "{{.AdminRoom}}": admin_room,
                "{{.UserRoom}}": user_room,
                "{{.KumoAdminRoom}}": admin_room,
            },
        },
        "kumo": {
            "template": Path(root_dir) / "templates/kumo-agent/gosuto.yaml",
            "subs": {
                "{{.AgentName}}": "kumo",
                "{{.AdminRoom}}": admin_room,
                "{{.UserRoom}}": user_room,
                "{{.KairoAdminRoom}}": admin_room,
                "{{.PeerAlias}}": "kairo",
                "{{.PeerMXID}}": "@kairo:localhost",
                "{{.PeerRoom}}": admin_room,
                "{{.PeerProtocolID}}": "kairo.news.request.v1",
                "{{.PeerProtocolPrefix}}": "KAIRO_NEWS_REQUEST",
            },
        },
    }

    cfg = specs[agent]
    text = cfg["template"].read_text(encoding="utf-8")
    for key, val in cfg["subs"].items():
        text = text.replace(key, val)

    if agent == "saito":
        cron_every = (saito_cron_every or "10s").strip()
        text = text.replace('expression: "*/15 * * * *"', f'expression: "@every {cron_every}"')

    cfg_hash = hashlib.sha256(text.encode("utf-8")).hexdigest()
    return text, cfg_hash


def discover_ip(agent: str) -> str:
    container = f"ruriko-agent-{agent}"
    cmd = ["docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container]
    out = subprocess.check_output(cmd, text=True).strip()
    if not out:
        raise RuntimeError(f"empty container IP for {container}")
    return out


def post_apply(endpoint: str, token: str, yaml_text: str, cfg_hash: str, timeout_seconds: int) -> tuple[int | None, str]:
    url = endpoint.rstrip("/") + "/config/apply"
    body = json.dumps({"yaml": yaml_text, "hash": cfg_hash}).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout_seconds) as resp:
            return resp.status, (resp.read() or b"").decode("utf-8", errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, (err.read() or b"").decode("utf-8", errors="replace")
    except Exception as err:  # noqa: BLE001
        return None, str(err)


def get_status_hash(endpoint: str, token: str) -> str:
    req = urllib.request.Request(
        endpoint.rstrip("/") + "/status",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
        method="GET",
    )
    with urllib.request.urlopen(req, timeout=8) as resp:
        if resp.status != 200:
            raise RuntimeError(f"status {resp.status}")
        data = json.loads((resp.read() or b"{}").decode("utf-8", errors="replace"))
    return ((data.get("config") or {}).get("hash") or "").strip()


def main() -> int:
    args = parse_args()

    agent_rows = load_agents(args.db_file)
    missing = [agent for agent in ("saito", "kairo", "kumo") if agent not in agent_rows]
    if missing:
        print(f"[bootstrap] missing agents in db: {', '.join(missing)}", file=sys.stderr)
        return 1

    for agent in ("saito", "kairo", "kumo"):
        control_url, token = agent_rows[agent]
        if not token:
            print(f"[bootstrap] missing control token for {agent}", file=sys.stderr)
            return 1

        yaml_text, want_hash = render_template(args.root_dir, agent, args.admin_room, args.user_room, args.saito_cron_every)

        endpoints: list[str] = []
        if control_url:
            endpoints.append(control_url)
        endpoints.append(f"http://{discover_ip(agent)}:8765")
        endpoints = list(dict.fromkeys(endpoints))

        accepted = False
        accepted_immediate = False
        hard_error = ""

        for endpoint in endpoints:
            status, body = post_apply(endpoint, token, yaml_text, want_hash, args.apply_timeout)
            if status == 200:
                print(f"[bootstrap] apply accepted for {agent} via {endpoint}")
                accepted = True
                accepted_immediate = True
                break
            if status == 422:
                hard_error = f"validation failed for {agent} via {endpoint}: {body}"
                break
            if status is None and ("timed out" in body.lower() or "timeout" in body.lower()):
                print(f"[bootstrap] apply timeout for {agent} via {endpoint}; treating as pending")
                accepted = True
                break
            print(f"[bootstrap] apply not accepted for {agent} via {endpoint}: status={status} body={body}")

        if hard_error:
            print(f"[bootstrap] {hard_error}", file=sys.stderr)
            return 1
        if not accepted:
            print(f"[bootstrap] failed to submit apply for {agent}", file=sys.stderr)
            return 1
        if accepted_immediate:
            continue

        deadline = time.time() + args.status_timeout
        converged = False
        last_seen = ""

        while time.time() < deadline:
            for endpoint in endpoints:
                try:
                    got = get_status_hash(endpoint, token)
                except Exception:  # noqa: BLE001
                    continue
                if got:
                    last_seen = got
                if got == want_hash:
                    print(f"[bootstrap] applied canonical config to {agent} via {endpoint}")
                    converged = True
                    break
            if converged:
                break
            time.sleep(args.poll_interval)

        if not converged:
            print(
                "[bootstrap] warning: timed out waiting for "
                f"{agent} hash convergence (want={want_hash} last={last_seen or 'none'}); continuing"
            )

    print("[bootstrap] canonical config bootstrap complete")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
