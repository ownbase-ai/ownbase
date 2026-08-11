package vault

import (
	"testing"
)

// TestSave_CreateConflictDoesNotOverwrite locks the contract that a create
// which loses its VersionNone Put refuses rather than retrying. Retrying
// would decode the winner and wipe its OwnBase group with the loser's empty
// one (Bugbot finding on the Store seam PR).
func TestSave_CreateConflictDoesNotOverwrite(t *testing.T) {
	store := NewMemStore("create-race")
	const pw = "pw"

	winner, err := CreateStore(store, pw)
	if err != nil {
		t.Fatalf("CreateStore winner: %v", err)
	}
	winner.Put("keep", Profile{Host: "h.example.com", Token: "secret"})
	if err := winner.Save(); err != nil {
		t.Fatalf("winner.Save: %v", err)
	}

	// Loser is the state CreateStore is in after its existence check raced
	// with the winner: VersionNone, empty profiles, store already populated.
	loser := &Vault{
		store:    store,
		password: pw,
		profiles: map[string]Profile{},
		db:       newDatabase(pw),
		version:  VersionNone,
	}
	if err := loser.Save(); err == nil {
		t.Fatal("loser.Save: got nil, want already-exists error")
	}

	final, err := OpenStore(store, pw)
	if err != nil {
		t.Fatalf("OpenStore after failed create: %v", err)
	}
	p, err := final.Get("keep")
	if err != nil {
		t.Fatalf("Get(keep): %v — winner was overwritten", err)
	}
	if p.Token != "secret" {
		t.Errorf("Token = %q, want secret (winner must be intact)", p.Token)
	}
}
