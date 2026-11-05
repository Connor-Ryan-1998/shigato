package core

import (
	"errors"
	"testing"
)

func TestValidABN(t *testing.T) {
	cases := []struct {
		name string
		abn  string
		want bool
	}{
		{"valid", "51 824 753 556", true},
		{"bad checksum", "51 824 753 557", false},
		{"too short", "5182475355", false},
		{"empty allowed", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidABN(c.abn); got != c.want {
				t.Errorf("ValidABN(%q) = %v, want %v", c.abn, got, c.want)
			}
		})
	}
}

func TestSettingsValidation(t *testing.T) {
	base := func() OrgSettings {
		return OrgSettings{Name: "Acme", InvoiceLeadDays: 7}
	}

	cases := []struct {
		name    string
		modify  func(OrgSettings) OrgSettings
		wantErr bool
	}{
		{
			name:    "bad bsb format",
			modify:  func(s OrgSettings) OrgSettings { s.BankBSB = "12-3456"; s.BankAccount = "12345678"; return s },
			wantErr: true,
		},
		{
			name:    "bsb without account",
			modify:  func(s OrgSettings) OrgSettings { s.BankBSB = "123-456"; return s },
			wantErr: true,
		},
		{
			name:    "payid mobile with space passes",
			modify:  func(s OrgSettings) OrgSettings { s.PayID = "0412 345 678"; return s },
			wantErr: false,
		},
		{
			name:    "payid garbage fails",
			modify:  func(s OrgSettings) OrgSettings { s.PayID = "bob@"; return s },
			wantErr: true,
		},
		{
			name:    "invoice lead days too high",
			modify:  func(s OrgSettings) OrgSettings { s.InvoiceLeadDays = 15; return s },
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateSettings(c.modify(base()))
			if c.wantErr && !errors.Is(err, ErrInvalidInput) {
				t.Errorf("validateSettings: err = %v, want ErrInvalidInput", err)
			}
			if !c.wantErr && err != nil {
				t.Errorf("validateSettings: err = %v, want nil", err)
			}
		})
	}
}
