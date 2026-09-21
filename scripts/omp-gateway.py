#!/usr/bin/env python3
"""Manage one loopback gateway backed by the current ZCode login.

The gateway reads the ZCode provider configuration directly; no credential is
copied, and the ZCode app files are never modified. Restart the gateway after
signing into ZCode again or switching plans.
"""
import argparse
import contextlib
import datetime
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import uuid
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPO = Path(__file__).resolve().parents[1]
ROOT = Path.home() / ".local/state/glm-zcode-2api"
ZCODE_CONFIG = Path.home() / ".zcode/v2/config.json"
BASE = "http://127.0.0.1:7864"
PORT = 7864
BINARY = REPO / "bin/glm-zcode-2api"
DEFAULT_PROVIDER = "builtin:bigmodel-coding-plan"


def secure_write(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(value)
        flush_and_sync(handle)
    os.replace(tmp, path)


def flush_and_sync(handle):
    handle.flush()
    os.fsync(handle.fileno())


def audit(action, **data):
    context_file = ROOT / "active-audit.json"
    if context_file.exists():
        context = json.loads(context_file.read_text())
        audit_id = context.get("audit_id", "glm-zcode-2api-local")
    else:
        audit_id = "glm-zcode-2api-local"
    record = dict(audit_id=audit_id, action=action,
                  time=datetime.datetime.now(datetime.timezone.utc).isoformat(), **data)
    fd = os.open(ROOT / "audit.jsonl", os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    with os.fdopen(fd, "a") as handle:
        handle.write(json.dumps(record) + "\n")
        flush_and_sync(handle)


def owned_pid():
    try:
        pid = int((ROOT / "gateway.pid").read_text())
        command = subprocess.run(["ps", "-p", str(pid), "-o", "command="],
                                 capture_output=True, text=True, check=False).stdout
        if str(BINARY) in command and str(ROOT / "config.json") in command:
            return pid
    except (OSError, ValueError):
        pass
    return None


def health():
    try:
        request = urllib.request.Request(BASE + "/healthz")
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(request, timeout=2) as response:
            data = json.load(response)
        return data if data.get("service") == "glm-zcode-2api" else None
    except (OSError, ValueError, urllib.error.URLError):
        return None


def select_provider(document):
    providers = document.get("provider") or {}
    preferred = os.environ.get("Z2A_UPSTREAM_PROVIDER_ID", DEFAULT_PROVIDER)
    entry = providers.get(preferred)
    if entry is None:
        raise RuntimeError(
            f"ZCode has no provider {preferred!r}; start ZCode and sign in first")
    options = entry.get("options") or {}
    if entry.get("systemDisabledReason"):
        raise RuntimeError(
            f"ZCode provider {preferred!r} is unavailable ({entry['systemDisabledReason']}); "
            "check your plan in ZCode")
    if not options.get("apiKey"):
        raise RuntimeError(
            f"ZCode provider {preferred!r} has no API key; sign in to ZCode again")
    if not options.get("baseURL"):
        raise RuntimeError(f"ZCode provider {preferred!r} has no base URL")
    return preferred, entry


def app_version():
    """Read the installed ZCode version; fall back to a recent known one."""
    plist = Path("/Applications/ZCode.app/Contents/Info.plist")
    try:
        import plistlib
        with plist.open("rb") as handle:
            data = plistlib.load(handle)
        version = str(data.get("CFBundleShortVersionString") or "")
        if version:
            return version
    except (OSError, ValueError, ImportError):
        pass
    return "3.14.0"


def models_from_provider(entry):
    models = []
    for model_id, spec in (entry.get("models") or {}).items():
        limit = spec.get("limit") or {}
        modalities = (spec.get("modalities") or {}).get("input") or []
        models.append({
            "id": model_id.lower(),
            "upstream": model_id,
            "name": model_id,
            "context_length": int(limit.get("context") or 128000),
            "max_output_tokens": int(limit.get("output") or 64000),
            "supports_images": "image" in modalities,
        })
    if not models:
        raise RuntimeError("the ZCode provider lists no models; open ZCode once to refresh it")
    return models


def device_id():
    """Persistent per-install device id, mirroring the official client's deviceMid."""
    path = ROOT / "device.key"
    if path.exists():
        value = path.read_text().strip()
        if value:
            return value
    value = str(uuid.uuid4())
    secure_write(path, value + "\n")
    return value


def prepare():
    try:
        document = json.loads(ZCODE_CONFIG.read_text())
    except (OSError, ValueError) as error:
        raise RuntimeError(f"cannot read the ZCode configuration: {error}")
    provider_id, entry = select_provider(document)
    models = models_from_provider(entry)

    key_file = ROOT / "client.key"
    if not key_file.exists():
        secure_write(key_file, secrets.token_urlsafe(32) + "\n")
    key = key_file.read_text().strip()
    if len(key) < 32:
        raise RuntimeError("invalid local client key")

    upstream_config = {
        "provider_id": provider_id,
        "credential_config_path": str(ZCODE_CONFIG),
        # Official coding-plan model traffic is routed through the ZCode platform
        # gateway, where plan entitlements are validated.
        "gateway_origin": "https://zcode.z.ai",
        "device_id": device_id(),
        "anthropic_version": "2023-06-01",
        # Identify proxied requests as the ZCode client so plan promotions
        # (off-peak discounts, free flash windows) apply the same way.
        "mimic_client": True,
        "app_version": app_version(),
        "timeout_seconds": 120,
        "header_timeout_seconds": 120,
        "idle_timeout_seconds": 300,
    }
    config = {
        "listen": f"0.0.0.0:{PORT}",
        "api_key": key,
        "server": {"max_body_mb": 16},
        "upstream": upstream_config,
        "thinking": {"enabled": True, "effort": "max", "prompt_cache": True},
        "models": models,
    }
    audit("prepare_gateway_config", source=str(ZCODE_CONFIG), provider=provider_id,
          models=[m["id"] for m in models], credential_copied=False)
    secure_write(ROOT / "config.json", json.dumps(config, indent=2) + "\n")
    return [m["id"] for m in models]


def start(wait=True, wait_seconds=40):
    pid = owned_pid()
    if pid:
        if wait:
            print(json.dumps({"running": True, "pid": pid, "health": health()}))
        return
    if not BINARY.is_file():
        raise RuntimeError("Build bin/glm-zcode-2api first")
    with socket.socket() as probe:
        if probe.connect_ex(("127.0.0.1", PORT)) == 0:
            raise RuntimeError(f"Port {PORT} is already occupied; no process was replaced")
    models = prepare()
    audit("start_gateway", address=BASE, models=models)
    log_fd = os.open(ROOT / "gateway.log", os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    with os.fdopen(log_fd, "ab") as log:
        process = subprocess.Popen([str(BINARY), "-config", str(ROOT / "config.json")],
                                   cwd=ROOT, stdin=subprocess.DEVNULL,
                                   stdout=log, stderr=log, start_new_session=True,
                                   env={k: v for k, v in os.environ.items()
                                        if not k.startswith("Z2A_")})
    try:
        secure_write(ROOT / "gateway.pid", str(process.pid) + "\n")
        for _ in range(wait_seconds * 4):
            if process.poll() is not None:
                raise RuntimeError("Gateway exited; inspect its local log")
            status = health()
            if status:
                audit("gateway_started", pid=process.pid, address=BASE)
                if wait:
                    print(json.dumps({"running": True, "pid": process.pid,
                                      "base_url": BASE + "/v1", "models": models}))
                return
            time.sleep(0.25)
        audit("gateway_start_timeout", pid=process.pid)
        if wait:
            raise RuntimeError("Gateway health check did not succeed")
    except BaseException:
        # Keep no half-started process alive if bookkeeping failed.
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        try:
            if (ROOT / "gateway.pid").read_text().strip() == str(process.pid):
                (ROOT / "gateway.pid").unlink()
        except OSError:
            pass
        raise


def stop():
    pid = owned_pid()
    if not pid:
        print("Gateway is not running")
        return
    audit("stop_gateway", pid=pid)
    os.kill(pid, signal.SIGTERM)
    for _ in range(60):
        if not owned_pid():
            (ROOT / "gateway.pid").unlink(missing_ok=True)
            print("Gateway stopped")
            return
        time.sleep(0.1)
    raise RuntimeError("Gateway is still shutting down; no stronger signal was sent")


def capture(port=7865):
    """Capture one live ZCode client request by pointing the client at us.

    Launch the client with ZCODE_ENDPOINT_ORIGIN=http://127.0.0.1:<port> and send
    any message; every model request (path, headers, body) is appended to
    capture.jsonl for byte-level comparison with what this gateway sends.
    """
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    target = ROOT / "capture.jsonl"

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self):
            self.send_response(200)
            self.send_header("content-type", "application/json")
            self.end_headers()
            self.wfile.write(b"{}")

        def do_POST(self):
            length = int(self.headers.get("content-length") or 0)
            body = self.rfile.read(length) if length else b""
            record = {
                "time": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                "method": self.command,
                "path": self.path,
                "headers": {k: v for k, v in self.headers.items()},
                "body_sha256": hashlib.sha256(body).hexdigest(),
                "body_preview": body[:2000].decode("utf-8", "replace"),
            }
            fd = os.open(target, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
            with os.fdopen(fd, "a") as handle:
                handle.write(json.dumps(record) + "\n")
                flush_and_sync(handle)
            print(f"captured {self.command} {self.path} ({len(body)} bytes) -> {target}", flush=True)
            payload = (
                'event: message_start\ndata: {"type":"message_start","message":{"id":"msg_capture",'
                '"type":"message","role":"assistant","content":[],"model":"capture",'
                '"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}\n\n'
                'event: content_block_start\ndata: {"type":"content_block_start","index":0,'
                '"content_block":{"type":"text","text":""}}\n\n'
                'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,'
                '"delta":{"type":"text_delta","text":"captured"}}\n\n'
                'event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n'
                'event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"},'
                '"usage":{"output_tokens":1}}\n\n'
                'event: message_stop\ndata: {"type":"message_stop"}\n\n'
            ).encode()
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"capture listener on http://127.0.0.1:{port} -> {target}")
    print("now launch the client against it, e.g.:")
    print(f"  ZCODE_ENDPOINT_ORIGIN=http://127.0.0.1:{port} open -a ZCode --env ZCODE_ENDPOINT_ORIGIN=http://127.0.0.1:{port}")
    print("then send any message in ZCode; Ctrl-C here when done.")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        print(f"stopped; captured requests are in {target}")



def print_offpeak_window(start_hour=23, end_hour=9):
    """闲时窗口默认按北京时间 23:00 → 次日 09:00 定义；窗口可配置。"""
    from zoneinfo import ZoneInfo
    bj_tz = ZoneInfo("Asia/Shanghai")
    now_bj = datetime.datetime.now(bj_tz)
    if start_hour <= end_hour:
        in_window = start_hour <= now_bj.hour < end_hour
    else:
        in_window = now_bj.hour >= start_hour or now_bj.hour < end_hour
    start_bj = now_bj.replace(hour=start_hour, minute=0, second=0, microsecond=0)
    if end_hour <= start_hour and now_bj.hour < end_hour:
        start_bj -= datetime.timedelta(days=1)
    end_bj = start_bj + datetime.timedelta(hours=(end_hour - start_hour) % 24 or 24)
    local_tz = datetime.datetime.now().astimezone().tzinfo
    local_start = start_bj.astimezone(local_tz).strftime("%m-%d %H:%M")
    local_end = end_bj.astimezone(local_tz).strftime("%m-%d %H:%M")
    state = "当前在窗口内" if in_window else "当前在窗口外"
    print(f"闲时窗口（北京时间 23:00–次日 09:00）：{state}"
          f"（当前北京时间 {now_bj.strftime('%m-%d %H:%M')}；"
          f"本窗口 {start_bj.strftime('%m-%d %H:%M')} → {end_bj.strftime('%m-%d %H:%M')}，"
          f"本地 {local_start} → {local_end}）")


def usage():
    """Query the plan's real credit meter (5h window + weekly) and today's usage."""
    key = (json.loads(ZCODE_CONFIG.read_text()).get("provider") or {})
    key = next(((v.get("options") or {}).get("apiKey") for v in key.values()
                if (v.get("options") or {}).get("apiKey")), "")
    if not key:
        raise RuntimeError("no plan API key found in the ZCode config")
    url = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
    req = urllib.request.Request(url, headers={"authorization": key})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(req, timeout=15) as response:
        envelope = json.load(response)
    data = envelope.get("data") or {}
    rows = []
    for limit in data.get("limits") or []:
        unit, number = limit.get("unit"), limit.get("number")
        label = {3: "5 小时窗口", 6: "周窗口"}.get(unit, f"unit={unit}")
        reset = datetime.datetime.fromtimestamp(limit.get("nextResetTime", 0) / 1000,
                                               datetime.timezone.utc).astimezone()
        rows.append((label, limit.get("currentValue"), limit.get("remaining"),
                     limit.get("percentage"), reset.strftime("%m-%d %H:%M")))
    print("套餐额度（level=%s）：" % data.get("level"))
    for label, used, remaining, pct, reset in rows:
        print(f"  {label}: 已用 {used} / 剩余 {remaining}（{pct}%），重置于 {reset}")
    print_offpeak_window()
    tz = datetime.datetime.now().astimezone().tzinfo
    today = datetime.datetime.now(tz).strftime("%Y-%m-%d")
    start = urllib.parse.quote(f"{today} 00:00:00")
    end = urllib.parse.quote(f"{today} 23:59:59")
    detail_url = ("https://open.bigmodel.cn/api/monitor/usage/model-usage"
                  f"?startTime={start}&endTime={end}")
    req = urllib.request.Request(detail_url, headers={"authorization": key})
    with opener.open(req, timeout=15) as response:
        detail = json.load(response).get("data") or {}
    buckets = list(zip(detail.get("x_time") or [], detail.get("modelCallCount") or [],
                       detail.get("tokensUsage") or []))
    nonempty = [(t, c, tk) for t, c, tk in buckets if c]
    calls = sum(c for _, c, _ in nonempty)
    tokens = sum(tk for _, _, tk in nonempty)
    print(f"今日（{tz}）模型调用 {calls} 次 / {tokens:,} tokens，分时：")
    for t, c, tk in nonempty[-6:]:
        print(f"  {t}  {c:>4} 次  {tk:>12,} tokens")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("start", "stop", "restart", "status", "token", "capture", "usage"))
    args = parser.parse_args()
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(ROOT, 0o700)
    with open(ROOT / "control.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if args.command == "token":
            # OMP consumes stdout as a credential and bounds how long the
            # command may take: never block on a cold start. The key is
            # printed immediately while the gateway comes up in parallel.
            with contextlib.redirect_stdout(io.StringIO()):
                start(wait=False, wait_seconds=2)
            print((ROOT / "client.key").read_text().strip())
        elif args.command == "status":
            print(json.dumps({"running": bool(owned_pid()), "health": health(),
                              "base_url": BASE + "/v1"}))
        elif args.command == "stop":
            stop()
        elif args.command == "usage":
            usage()
        elif args.command == "capture":
            capture()
        elif args.command == "restart":
            stop()
            start()
        else:
            start()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Never dump request objects or raw credential records.
        print(f"Gateway control failed: {error}", file=sys.stderr)
        sys.exit(1)
