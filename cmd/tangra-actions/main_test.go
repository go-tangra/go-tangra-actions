package main

import "testing"

func TestParseArgs(t *testing.T) {
	cfg, path, err := parseArgs([]string{
		"-input", "service=nginx",
		"-input", "domain=example.com",
		"-secret", "tok",
		"-actions", "./acts",
		"-confine", "/srv/ws",
		"flow.yaml",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if path != "flow.yaml" {
		t.Errorf("path = %q", path)
	}
	if cfg.inputs["service"] != "nginx" || cfg.inputs["domain"] != "example.com" {
		t.Errorf("inputs = %v", cfg.inputs)
	}
	if len(cfg.secrets) != 1 || cfg.secrets[0] != "tok" {
		t.Errorf("secrets = %v", cfg.secrets)
	}
	if cfg.actionsDir != "./acts" || cfg.confine != "/srv/ws" {
		t.Errorf("actionsDir/confine = %q/%q", cfg.actionsDir, cfg.confine)
	}
}

func TestParseArgs_Errors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no file", []string{"-input", "a=b"}},
		{"bad input", []string{"-input", "noequals", "f.yaml"}},
		{"empty key", []string{"-input", "=v", "f.yaml"}},
		{"unknown flag", []string{"-nope", "f.yaml"}},
		{"missing value", []string{"-input"}},
		{"two files", []string{"a.yaml", "b.yaml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseArgs(tt.args); err == nil {
				t.Errorf("parseArgs(%v) = nil error, want error", tt.args)
			}
		})
	}
}

func TestMark(t *testing.T) {
	cases := map[string]string{
		"success": "✓", "failure": "✗", "skipped": "∅", "weird": "?",
	}
	for status, want := range cases {
		if got := mark(status); got != want {
			t.Errorf("mark(%q) = %q, want %q", status, got, want)
		}
	}
}
