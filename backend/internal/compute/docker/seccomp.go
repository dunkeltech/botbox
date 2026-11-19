// SPDX-License-Identifier: AGPL-3.0-only

package docker

import _ "embed"

// Docker's baseline is kept in full. Only Chromium's exact namespace setup
// operations are added. See ADR-0038 for the pinned upstream revision.
//
//go:embed chromium-seccomp.json
var chromiumSeccompProfile string
