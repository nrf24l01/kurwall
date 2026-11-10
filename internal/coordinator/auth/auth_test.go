package auth

import "testing"

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("expected the password to verify")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("expected the wrong password to fail")
	}
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("expected a short password to be rejected")
	}
}

func TestUsernameValidation(t *testing.T) {
	valid := []string{"admin", "ops.team", "node_1", "a-b", "Upper"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Fatalf("expected %q to be valid: %v", name, err)
		}
	}
	invalid := []string{"", "ab", "with space", "bad/slash", "-leading"}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestTokens(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !ValidTokenFormat(token) {
		t.Fatalf("generated token %q has an invalid format", token)
	}
	if len(hash) != 32 {
		t.Fatalf("expected a 32-byte hash, got %d", len(hash))
	}
	if string(HashToken(token)) != string(hash) {
		t.Fatal("token hash is not deterministic")
	}
	if ValidTokenFormat("short") || ValidTokenFormat("bad token") {
		t.Fatal("expected malformed tokens to be rejected")
	}
}
