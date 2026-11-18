# SPDX-License-Identifier: AGPL-3.0-only

"""Contract tests for Chromium profile lock cleanup and crash recovery."""

from __future__ import annotations

import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile

SANDBOX_DIR = pathlib.Path(
    os.environ.get("BOTBOX_SANDBOX_DIR", str(pathlib.Path(__file__).resolve().parents[3] / "images" / "sandbox"))
)
LAUNCHER = SANDBOX_DIR / "chromium-launch.sh"
ENTRYPOINT = SANDBOX_DIR / "entrypoint.sh"
SUPERVISORD_CONF = SANDBOX_DIR / "supervisord.conf"


def make_fake_chromium(directory: pathlib.Path, args_out: pathlib.Path) -> pathlib.Path:
    """Create a stub chromium binary that records argv and lock presence."""
    stub = directory / "fake-chromium"
    stub.write_text(
        f"#!{sys.executable}\n"
        "import json, os, pathlib, sys\n"
        f"out = pathlib.Path({str(args_out)!r})\n"
        "user_data = os.environ['CHROMIUM_USER_DATA_DIR']\n"
        "locks = [n for n in ('SingletonLock', 'SingletonSocket', 'SingletonCookie') "
        "if os.path.lexists(os.path.join(user_data, n))]\n"
        "out.write_text(json.dumps({'argv': sys.argv[1:], 'locks': locks}))\n",
        encoding="utf-8",
    )
    stub.chmod(0o755)
    return stub


def make_fake_socket(path: pathlib.Path) -> None:
    """Create a real AF_UNIX socket file so bash [ -S ... ] succeeds."""
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sock.bind(str(path))
    finally:
        sock.close()


def seed_stale_profile(user_data_dir: pathlib.Path, lock_target: str, prefs: str | None) -> None:
    """Populate a Chromium profile directory with singleton locks and Preferences."""
    default_dir = user_data_dir / "Default"
    default_dir.mkdir(parents=True, exist_ok=True)
    os.symlink(lock_target, user_data_dir / "SingletonLock")
    os.symlink("/tmp/.org.chromium.Chromium.old/SingletonSocket", user_data_dir / "SingletonSocket")
    os.symlink("9999999999999999999", user_data_dir / "SingletonCookie")
    if prefs is not None:
        (default_dir / "Preferences").write_text(prefs, encoding="utf-8")


def write_fake_proc(proc_root: pathlib.Path, pid: int, comm: str, argv: list[str]) -> None:
    """Populate minimal /proc/<pid> entries owned by the current user."""
    pid_dir = proc_root / str(pid)
    pid_dir.mkdir(parents=True, exist_ok=True)
    (pid_dir / "comm").write_text(f"{comm}\n", encoding="utf-8")
    (pid_dir / "cmdline").write_bytes(b"\x00".join(a.encode() for a in argv) + b"\x00")


def run_launcher(
    extra_args: list[str],
    *,
    home_dir: pathlib.Path,
    user_data_dir: pathlib.Path,
    proc_root: pathlib.Path,
    chromium_bin: pathlib.Path | None = None,
    x11_socket: pathlib.Path | None = None,
) -> subprocess.CompletedProcess[str]:
    """Run chromium-launch.sh in an isolated environment."""
    env = {
        "PATH": os.environ["PATH"],
        "HOME": str(home_dir),
        "CHROMIUM_USER_DATA_DIR": str(user_data_dir),
        "BOTBOX_PROC_ROOT": str(proc_root),
        "BOTBOX_PYTHON_BIN": sys.executable,
    }
    if chromium_bin is not None:
        env["CHROMIUM_BIN"] = str(chromium_bin)
    if x11_socket is not None:
        env["BOTBOX_X11_SOCKET"] = str(x11_socket)
    return subprocess.run(
        [str(LAUNCHER), *extra_args],
        env=env,
        text=True,
        capture_output=True,
        timeout=15,
        check=False,
    )


def test_cleans_stale_locks_and_crash_prefs_on_launch() -> None:
    assert LAUNCHER.is_file(), f"missing launcher script: {LAUNCHER}"
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        home_dir = root / "home"
        user_data_dir = home_dir / ".config" / "chromium"
        proc_root = root / "proc"
        proc_root.mkdir()
        # Simulate PID 32 reused by Xvnc after container restart on a new hostname.
        write_fake_proc(proc_root, 32, "Xtigervnc", ["/usr/bin/Xtigervnc", ":0"])

        prefs = json.dumps({
            "profile": {"exit_type": "Crashed", "exited_cleanly": False},
            "browser": {"show_home_button": True},
        })
        seed_stale_profile(user_data_dir, "26a623ebd3a7-32", prefs)

        x11_socket = root / "X0"
        make_fake_socket(x11_socket)
        args_out = root / "chromium-invoked.json"
        fake_bin = make_fake_chromium(root, args_out)

        res = run_launcher(
            [],
            home_dir=home_dir,
            user_data_dir=user_data_dir,
            proc_root=proc_root,
            chromium_bin=fake_bin,
            x11_socket=x11_socket,
        )
        assert res.returncode == 0, f"launcher failed: {res.stderr}"
        invoked = json.loads(args_out.read_text(encoding="utf-8"))
        assert invoked["locks"] == [], f"locks still existed at exec: {invoked['locks']}"
        for flag in (
            "--no-sandbox",
            "--disable-gpu",
            "--disable-dev-shm-usage",
            "--no-first-run",
            "--no-default-browser-check",
            "--remote-debugging-address=127.0.0.1",
            "--remote-debugging-port=9222",
            f"--user-data-dir={user_data_dir}",
            "--start-maximized",
            "about:blank",
        ):
            assert flag in invoked["argv"], f"missing flag {flag} in {invoked['argv']}"

        updated = json.loads((user_data_dir / "Default" / "Preferences").read_text(encoding="utf-8"))
        assert updated["profile"]["exit_type"] == "Normal"
        assert updated["profile"]["exited_cleanly"] is True
        assert updated["browser"]["show_home_button"] is True


def test_prepare_only_works_without_x11_socket() -> None:
    assert LAUNCHER.is_file(), f"missing launcher script: {LAUNCHER}"
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        home_dir = root / "home"
        user_data_dir = home_dir / ".config" / "chromium"
        proc_root = root / "proc"
        proc_root.mkdir()
        seed_stale_profile(
            user_data_dir,
            f"{socket.gethostname()}-999999",
            json.dumps({"profile": {"exit_type": "Crashed", "exited_cleanly": False}}),
        )

        res = run_launcher(
            ["--prepare-only"],
            home_dir=home_dir,
            user_data_dir=user_data_dir,
            proc_root=proc_root,
            x11_socket=root / "missing-X0",
        )
        assert res.returncode == 0, f"--prepare-only failed: {res.stderr}"
        for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
            assert not os.path.lexists(user_data_dir / name), f"{name} was not removed"


def test_preserves_locks_when_active_chromium_owns_profile() -> None:
    assert LAUNCHER.is_file(), f"missing launcher script: {LAUNCHER}"
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        home_dir = root / "home"
        user_data_dir = home_dir / ".config" / "chromium"
        proc_root = root / "proc"
        proc_root.mkdir()
        write_fake_proc(
            proc_root,
            4242,
            "chromium",
            ["/usr/lib/chromium/chromium", f"--user-data-dir={user_data_dir}"],
        )
        prefs = json.dumps({"profile": {"exit_type": "Crashed", "exited_cleanly": False}})
        seed_stale_profile(user_data_dir, f"{socket.gethostname()}-4242", prefs)

        res = run_launcher(
            ["--prepare-only"],
            home_dir=home_dir,
            user_data_dir=user_data_dir,
            proc_root=proc_root,
        )
        assert res.returncode == 0, f"--prepare-only failed: {res.stderr}"
        for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
            assert os.path.lexists(user_data_dir / name), f"active {name} was wrongly removed"
        unchanged = json.loads((user_data_dir / "Default" / "Preferences").read_text(encoding="utf-8"))
        assert unchanged["profile"]["exit_type"] == "Crashed"


def test_cleans_locks_when_unrelated_chromium_uses_different_profile() -> None:
    assert LAUNCHER.is_file(), f"missing launcher script: {LAUNCHER}"
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        home_dir = root / "home"
        user_data_dir = home_dir / ".config" / "chromium"
        proc_root = root / "proc"
        proc_root.mkdir()
        write_fake_proc(
            proc_root,
            5151,
            "chromium",
            ["/usr/lib/chromium/chromium", f"--user-data-dir={root / 'other-profile'}"],
        )
        seed_stale_profile(user_data_dir, "old-container-12", None)

        res = run_launcher(
            ["--prepare-only"],
            home_dir=home_dir,
            user_data_dir=user_data_dir,
            proc_root=proc_root,
        )
        assert res.returncode == 0, f"--prepare-only failed: {res.stderr}"
        for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
            assert not os.path.lexists(user_data_dir / name), f"stale {name} was not removed"


def test_survives_corrupt_preferences_json() -> None:
    assert LAUNCHER.is_file(), f"missing launcher script: {LAUNCHER}"
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        home_dir = root / "home"
        user_data_dir = home_dir / ".config" / "chromium"
        proc_root = root / "proc"
        proc_root.mkdir()
        seed_stale_profile(user_data_dir, "26a623ebd3a7-32", "{not valid json")

        res = run_launcher(
            ["--prepare-only"],
            home_dir=home_dir,
            user_data_dir=user_data_dir,
            proc_root=proc_root,
        )
        assert res.returncode == 0, f"failed on corrupt Preferences: {res.stderr}"
        assert not os.path.lexists(user_data_dir / "SingletonLock")
        assert (user_data_dir / "Default" / "Preferences").read_text(encoding="utf-8") == "{not valid json"


def test_entrypoint_and_supervisord_wire_chromium_launcher() -> None:
    entrypoint_text = ENTRYPOINT.read_text(encoding="utf-8")
    supervisord_text = SUPERVISORD_CONF.read_text(encoding="utf-8")
    assert "/usr/local/bin/chromium-launch.sh --prepare-only" in entrypoint_text
    assert "command=/usr/local/bin/chromium-launch.sh" in supervisord_text


def main() -> int:
    tests = [
        test_cleans_stale_locks_and_crash_prefs_on_launch,
        test_prepare_only_works_without_x11_socket,
        test_preserves_locks_when_active_chromium_owns_profile,
        test_cleans_locks_when_unrelated_chromium_uses_different_profile,
        test_survives_corrupt_preferences_json,
        test_entrypoint_and_supervisord_wire_chromium_launcher,
    ]
    for fn in tests:
        fn()
    print(f"chromium profile contract: {len(tests)} tests passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
