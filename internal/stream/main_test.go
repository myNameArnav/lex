package stream

import (
	"os"
	"testing"

	"lex/internal/store"
)

// TestMain hashes passwords at bcrypt.MinCost: DefaultCost makes every
// user created and login tried slow, far slower under -race.
func TestMain(m *testing.M) {
	store.UseMinPasswordCostForTests()
	os.Exit(m.Run())
}
