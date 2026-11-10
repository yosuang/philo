package auth

import "testing"

func TestParseAndAuthenticate(t *testing.T) {
	creds, err := ParseCredentials("one|actor-a|owner-a, two|actor-b|owner-b")
	if err != nil {
		t.Fatal(err)
	}
	authn := New(creds)
	got, err := authn.Authenticate("Bearer one")
	if err != nil || got.ActorID != "actor-a" || got.OwnerID != "owner-a" {
		t.Fatalf("principal = %+v err=%v", got, err)
	}
	if _, err := authn.Authenticate("Bearer missing"); err == nil {
		t.Fatal("missing token was accepted")
	}
	if _, err := authn.Authenticate(""); err == nil {
		t.Fatal("empty header was accepted")
	}
}

func TestEmptyCredentialsAcceptNobody(t *testing.T) {
	creds, err := ParseCredentials("  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(creds).Authenticate("Bearer one"); err == nil {
		t.Fatal("unconfigured authenticator accepted a token")
	}
}

func TestMalformedCredentials(t *testing.T) {
	if _, err := ParseCredentials("only-a-token"); err == nil {
		t.Fatal("malformed credentials were accepted")
	}
}
