package store

import (
	"os"
	"testing"
)

// TestMain hashes passwords at bcrypt.MinCost: DefaultCost makes every
// user created and login tried slow, far slower under -race.
func TestMain(m *testing.M) {
	UseMinPasswordCostForTests()
	os.Exit(m.Run())
}
