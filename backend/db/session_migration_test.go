// SPDX-License-Identifier: AGPL-3.0-only

package migrations_test

import (
	"testing"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
)

func TestRefreshFamilyUpgradePreservesLegacySessionStatus(t *testing.T) {
	pool := pgtest.OpenAt(t, 31)
	ctx := t.Context()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(email, password_hash, display_name)
		VALUES ('legacy@example.test', 'fixture-hash', 'Legacy') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	active, revoked := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, revoked_at)
		VALUES ($1, $2, $4, now() + interval '1 day', now()),
		       ($1, $2, $5, now() + interval '1 day', NULL),
		       ($1, $3, $6, now() + interval '1 day', now())`,
		userID, active, revoked, []byte("replaced"), []byte("current"), []byte("revoked"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		family  uuid.UUID
		revoked bool
	}{{active, false}, {revoked, true}} {
		var actual bool
		if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM refresh_token_families WHERE id = $1`, tc.family).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != tc.revoked {
			t.Fatalf("family %s revoked = %v, want %v", tc.family, actual, tc.revoked)
		}
	}
}
