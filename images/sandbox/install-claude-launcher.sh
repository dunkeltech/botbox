#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only

# npm links /usr/local/bin/claude to the package binary (cli.js on 1.0.51,
# bin/claude.exe on 2.1.323). Copying onto that path follows the link and
# replaces the official CLI. Remove the link first and install a regular file.
set -euo pipefail
dest="${1:?destination}"
src="${2:?source}"
if [[ ! -f "$src" ]]; then
  echo "claude launcher source is missing" >&2
  exit 1
fi
rm -f "$dest"
cp "$src" "$dest"
chmod 0755 "$dest"
