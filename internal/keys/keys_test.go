package keys

import (
	"bytes"
	"io"
	"testing"

	"filippo.io/age"
)

func TestBothKeysDecrypt(t *testing.T) {
	dir := t.TempDir()
	rec, err := Init(dir, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	recips, err := Recipients(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ct bytes.Buffer
	w, _ := age.Encrypt(&ct, recips...)
	io.WriteString(w, "secret data")
	w.Close()

	daily, err := DailyIdentity(dir, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := ParseRecovery(rec)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]age.Identity{"daily": daily, "recovery": recovery} {
		r, err := age.Decrypt(bytes.NewReader(ct.Bytes()), id)
		if err != nil {
			t.Fatalf("%s key cannot decrypt: %v", name, err)
		}
		b, _ := io.ReadAll(r)
		if string(b) != "secret data" {
			t.Fatalf("%s key: wrong plaintext", name)
		}
	}
	if _, err := DailyIdentity(dir, "wrong password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := Init(dir, "x"); err == nil {
		t.Fatal("Init must refuse to overwrite existing keys")
	}
}

func TestChangePassword(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, "old password 123"); err != nil {
		t.Fatal(err)
	}
	before, _ := DailyIdentity(dir, "old password 123")
	if err := ChangePassword(dir, "old password 123", "new password 456"); err != nil {
		t.Fatal(err)
	}
	after, err := DailyIdentity(dir, "new password 456")
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("changing the password must keep the same key")
	}
}
