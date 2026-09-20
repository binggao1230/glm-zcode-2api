#!/usr/bin/env python3
"""Manage one loopback gateway backed by the current Z Code login.

The gateway reads the Z Code provider configuration directly; no credential is
copied, and the Z Code app files are never modified. Restart the gateway after
signing into Z Code again or switching plans.
"""
import argparse
import contextlib
import datetime
import fcntl
import io
import json
import os
from pathlib import Path
import secrets
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
            f"Z Code has no provider {preferred!r}; start Z Code and sign in first")
    options = entry.get("options") or {}
    if entry.get("systemDisabledReason"):
        raise RuntimeError(
            f"Z Code provider {preferred!r} is unavailable ({entry['systemDisabledReason']}); "
            "check your plan in Z Code")
    if not options.get("apiKey"):
        raise RuntimeError(
            f"Z Code provider {preferred!r} has no API key; sign in to Z Code again")
    if not options.get("baseURL"):
        raise RuntimeError(f"Z Code provider {preferred!r} has no base URL")
    return preferred, entry


def app_version():
    """Read the installed Z Code version; fall back to a recent known one."""
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
        raise RuntimeError("the Z Code provider lists no models; open Z Code once to refresh it")
    return models


def account_user_id(document):
    """Recover the Zhipu account id from any plan JWT in the Z Code config."""
    import base64
    for entry in (document.get("provider") or {}).values():
        candidate = ((entry.get("options") or {}).get("apiKey") or "")
        if candidate.count(".") != 2:
            continue
        middle = candidate.split(".")[1]
        try:
            claims = json.loads(base64.urlsafe_b64decode(middle + "=" * (-len(middle) % 4)))
        except (ValueError, json.JSONDecodeError):
            continue
        user_id = claims.get("user_id")
        if user_id:
            return str(user_id)
    return None


def prepare():
    try:
        document = json.loads(ZCODE_CONFIG.read_text())
    except (OSError, ValueError) as error:
        raise RuntimeError(f"cannot read the Z Code configuration: {error}")
    provider_id, entry = select_provider(document)
    models = models_from_provider(entry)

    key_file = ROOT / "client.key"
    if not key_file.exists():
        secure_write(key_file, secrets.token_urlsafe(32) + "\n")
    key = key_file.read_text().strip()
    if len(key) < 32:
        raise RuntimeError("invalid local client key")

    user_id = account_user_id(document)
    upstream_config = {
        "provider_id": provider_id,
        "credential_config_path": str(ZCODE_CONFIG),
        "anthropic_version": "2023-06-01",
        # Identify proxied requests as the Z Code client so plan promotions
        # (off-peak discounts, free flash windows) apply the same way.
        "mimic_client": True,
        "app_version": app_version(),
        "timeout_seconds": 120,
        "header_timeout_seconds": 120,
        "idle_timeout_seconds": 300,
    }
    if user_id:
        upstream_config["user_id"] = user_id

    config = {
        "listen": f"0.0.0.0:{PORT}",
        "api_key": key,
        "server": {"max_body_mb": 16},
        "upstream": upstream_config,
        "thinking": {"enabled": True, "effort": "max", "prompt_cache": True},
        "models": models,
    }
    audit("prepare_gateway_config", source=str(ZCODE_CONFIG), provider=provider_id,
          models=[m["id"] for m in models], credential_copied=False, user_id=user_id)
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("start", "stop", "restart", "status", "token"))
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
