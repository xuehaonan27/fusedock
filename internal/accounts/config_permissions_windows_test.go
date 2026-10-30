package accounts

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigFileDACL(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	if err := store.Add(ctx, Account{Name: "fixture", Provider: "pku", Username: "fixture"}, map[string][]byte{"password": []byte("fixture")}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err != nil {
		t.Fatalf("private config rejected: %v", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(store.Path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("world-readable account store accepted")
	}
}
