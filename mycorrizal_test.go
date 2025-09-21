package mycorrizal

import "testing"

func TestNew(t *testing.T) {
	cfg := &Config{}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if app == nil {
		t.Fatal("app is nil")
	}
}

func TestNewWithDefaultConfig(t *testing.T) {
	cfg := GetDefaultConfig()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if app == nil {
		t.Fatal("app is nil")
	}
}
