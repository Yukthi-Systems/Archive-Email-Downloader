/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 */

package db

import (
	"testing"
)

func TestParseLocation(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    *ParsedLocation
		expectError bool
	}{
		{
			name:  "Valid location format",
			input: "192.168.1.100:org123:example.com:2026-07-21:file456",
			expected: &ParsedLocation{
				ServerIP: "192.168.1.100",
				OrgID:    "org123",
				Domain:   "example.com",
				Date:     "2026-07-21",
				ID:       "file456",
			},
			expectError: false,
		},
		{
			name:        "Invalid location too few parts",
			input:       "192.168.1.100:org123",
			expected:    nil,
			expectError: true,
		},
		{
			name:        "Invalid location empty string",
			input:       "",
			expected:    nil,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ParseLocation(tt.input)
			if (err != nil) != tt.expectError {
				t.Fatalf("expected error: %v, got: %v", tt.expectError, err)
			}

			if !tt.expectError {
				if res.ServerIP != tt.expected.ServerIP ||
					res.OrgID != tt.expected.OrgID ||
					res.Domain != tt.expected.Domain ||
					res.Date != tt.expected.Date ||
					res.ID != tt.expected.ID {
					t.Errorf("expected %+v, got %+v", tt.expected, res)
				}
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	loc := &ParsedLocation{
		ServerIP: "10.0.0.1",
		OrgID:    "companyA",
		Domain:   "mail.com",
		Date:     "2026-05-15",
		ID:       "email999",
	}

	expectedURL := "http://10.0.0.1:8686/archive/read/companyA/mail.com/2026-05-15/email999"
	got := loc.BuildURL()

	if got != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, got)
	}
}
