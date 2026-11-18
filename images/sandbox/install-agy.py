# SPDX-License-Identifier: AGPL-3.0-only

"""Install a manifest-pinned official agy binary during the image build."""
import hashlib
import json
import pathlib
import platform
import shutil
import tarfile
import tempfile
import urllib.request

machine = platform.machine()
arch = {"x86_64": "amd64", "aarch64": "arm64"}[machine]
manifest = json.loads(pathlib.Path(f"/tmp/agy-linux-{arch}.json").read_text())
with tempfile.TemporaryDirectory() as directory:
    archive = pathlib.Path(directory) / "agy.tar.gz"
    with urllib.request.urlopen(manifest["url"], timeout=120) as response:
        with archive.open("wb") as output:
            shutil.copyfileobj(response, output)
    with archive.open("rb") as downloaded:
        digest = hashlib.file_digest(downloaded, "sha512").hexdigest()
    if digest != manifest["sha512"]:
        raise RuntimeError("agy release checksum mismatch")
    with tarfile.open(archive) as bundle:
        source = bundle.extractfile("antigravity")
        if source is None:
            raise RuntimeError("agy release executable missing")
        with pathlib.Path("/usr/local/bin/agy").open("wb") as target:
            shutil.copyfileobj(source, target)
pathlib.Path("/usr/local/bin/agy").chmod(0o755)
