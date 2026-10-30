#!/usr/bin/env bash
set -euo pipefail

# Clean stale Chromium singleton locks and crash flags when no live Chromium
# instance owns the profile directory. Persistent /home/bot volumes keep
# SingletonLock across container recreations (hostname changes) and unclean
# stops when Xvnc terminates before Chromium finishes closing its profile.

HOME_DIR="${HOME:-/home/bot}"
USER_DATA_DIR="${CHROMIUM_USER_DATA_DIR:-${HOME_DIR}/.config/chromium}"
CHROMIUM_BIN="${CHROMIUM_BIN:-chromium}"
X11_SOCKET="${BOTBOX_X11_SOCKET:-/tmp/.X11-unix/X0}"
PROC_ROOT="${BOTBOX_PROC_ROOT:-/proc}"
PYTHON_BIN="${BOTBOX_PYTHON_BIN:-python3}"

prepare_chromium_profile() {
  "${PYTHON_BIN}" - "${HOME_DIR}" "${USER_DATA_DIR}" "${PROC_ROOT}" <<'PY'
import json
import os
import socket
import sys

home_dir, user_data_dir, proc_root = sys.argv[1:4]
chromium_names = {"chromium", "chromium-browser", "chromium-browse", "chrome", "google-chrome", "headless_shell"}


def read_proc_info(proc_dir: str):
    try:
        comm = open(os.path.join(proc_dir, "comm"), encoding="utf-8", errors="ignore").read().strip()
        raw_cmd = open(os.path.join(proc_dir, "cmdline"), "rb").read()
    except OSError:
        return None
    args = [part.decode("utf-8", errors="ignore") for part in raw_cmd.split(b"\x00") if part]
    if not args:
        return None
    if any(arg.startswith("--type=") for arg in args[1:]):
        return None
    exe_name = os.path.basename(args[0])
    if comm not in chromium_names and exe_name not in chromium_names:
        return None
    return args


def extract_user_data_dir(args: list[str]):
    for idx, arg in enumerate(args[1:], start=1):
        if arg.startswith("--user-data-dir="):
            return arg.split("=", 1)[1]
        if arg == "--user-data-dir" and idx + 1 < len(args):
            return args[idx + 1]
    return None


def is_profile_active() -> bool:
    hostname = socket.gethostname()
    lock_path = os.path.join(user_data_dir, "SingletonLock")
    if os.path.islink(lock_path):
        try:
            target = os.readlink(lock_path)
        except OSError:
            target = ""
        host, sep, pid_str = target.rpartition("-")
        if sep and host == hostname and pid_str.isdigit():
            if read_proc_info(os.path.join(proc_root, pid_str)) is not None:
                return True

    if not os.path.isdir(proc_root):
        return False

    target_real = os.path.realpath(user_data_dir)
    default_real = os.path.realpath(os.path.join(home_dir, ".config", "chromium"))
    uid = os.getuid()
    try:
        entries = os.listdir(proc_root)
    except OSError:
        return False

    for entry in entries:
        if not entry.isdigit():
            continue
        proc_dir = os.path.join(proc_root, entry)
        try:
            if os.stat(proc_dir).st_uid != uid:
                continue
        except OSError:
            continue
        args = read_proc_info(proc_dir)
        if args is None:
            continue
        proc_dir_arg = extract_user_data_dir(args)
        if proc_dir_arg is not None:
            if os.path.realpath(proc_dir_arg) == target_real:
                return True
        elif target_real == default_real:
            return True
    return False


if is_profile_active():
    sys.exit(0)

for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
    path = os.path.join(user_data_dir, name)
    if os.path.lexists(path):
        try:
            os.unlink(path)
        except OSError:
            pass

prefs_path = os.path.join(user_data_dir, "Default", "Preferences")
if os.path.isfile(prefs_path):
    try:
        with open(prefs_path, encoding="utf-8") as handle:
            data = json.load(handle)
    except (OSError, json.JSONDecodeError):
        data = None
    if isinstance(data, dict):
        profile = data.get("profile")
        if profile is None:
            profile = {}
            data["profile"] = profile
        if isinstance(profile, dict):
            changed = (
                profile.get("exit_type") != "Normal"
                or profile.get("exited_cleanly") is not True
            )
            if changed:
                profile["exit_type"] = "Normal"
                profile["exited_cleanly"] = True
                tmp_path = prefs_path + ".tmp"
                with open(tmp_path, "w", encoding="utf-8") as handle:
                    json.dump(data, handle)
                os.replace(tmp_path, prefs_path)
PY
}

prepare_chromium_profile

if [[ "${1:-}" == "--prepare-only" ]]; then
  exit 0
fi

for _ in $(seq 1 50); do
  [[ -S "${X11_SOCKET}" ]] && break
  sleep 0.1
done

exec "${CHROMIUM_BIN}" \
  --no-sandbox \
  --disable-gpu \
  --disable-dev-shm-usage \
  --no-first-run \
  --no-default-browser-check \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir="${USER_DATA_DIR}" \
  --start-maximized \
  about:blank
