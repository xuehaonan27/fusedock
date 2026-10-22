package provider

import (
	"context"
	"testing"
)

type fakeFactory struct{ id string }

func (f fakeFactory) Descriptor() Descriptor {
	return Descriptor{ContractVersion: ContractVersion, ID: f.id, Name: f.id}
}
func (fakeFactory) Open(context.Context, OpenRequest) (Session, error) {
	return nil, ErrUnsupported
}

func TestRegistryRejectsDuplicatesAndInvalidIDs(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakeFactory{id: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeFactory{id: "alpha"}); err == nil {
		t.Fatal("duplicate provider was accepted")
	}
	if err := registry.Register(fakeFactory{id: "Not Valid"}); err == nil {
		t.Fatal("invalid provider ID was accepted")
	}
	if err := registry.Register(fakeFactory{id: "old"}); err != nil {
		t.Fatal(err)
	}
	if factory, ok := registry.Get("alpha"); !ok || factory.Descriptor().Name != "alpha" {
		t.Fatal("registered provider was not found")
	}
}

func TestRegistryValidatesDescriptorKeys(t *testing.T) {
	registry := NewRegistry()
	factory := duplicateFactory{}
	if err := registry.Register(factory); err == nil {
		t.Fatal("duplicate descriptor key was accepted")
	}
}

type duplicateFactory struct{}

func (duplicateFactory) Descriptor() Descriptor {
	return Descriptor{
		ContractVersion: ContractVersion, ID: "duplicate", Name: "Duplicate",
		Options:     []OptionSpec{{Key: "token", Description: "option"}},
		Credentials: []CredentialSpec{{Key: "token", Description: "credential"}},
	}
}
func (duplicateFactory) Open(context.Context, OpenRequest) (Session, error) {
	return nil, ErrUnsupported
}

func TestRegistryRejectsUnsupportedContract(t *testing.T) {
	registry := NewRegistry()
	factory := contractFactory{fakeFactory: fakeFactory{id: "future"}, version: ContractVersion + 1}
	if err := registry.Register(factory); err == nil {
		t.Fatal("future contract version was accepted")
	}
}

type contractFactory struct {
	fakeFactory
	version uint32
}

func (f contractFactory) Descriptor() Descriptor {
	d := f.fakeFactory.Descriptor()
	d.ContractVersion = f.version
	return d
}

func TestCleanRelativePath(t *testing.T) {
	valid := []string{"", "/", "docs", "/docs/report.pdf", "照片/2026"}
	for _, path := range valid {
		if _, err := CleanRelativePath(path); err != nil {
			t.Errorf("rejected valid path %q: %v", path, err)
		}
	}
	invalid := []string{"../secret", "docs/../../secret", "docs/./x", "bad\x00name", "bad\nname"}
	for _, path := range invalid {
		if _, err := CleanRelativePath(path); err == nil {
			t.Errorf("accepted invalid path %q", path)
		}
	}
}

func TestNamesPreserveSpaces(t *testing.T) {
	parts, err := CleanRelativePath(" docs/report final.pdf ")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0] != " docs" || parts[1] != "report final.pdf " {
		t.Fatalf("unexpected path parts: %#v", parts)
	}
}
