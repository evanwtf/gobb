package bundle_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// A bundle with no member addresses leaves the file and its count out, and
// Each reads no row of it.
func TestMemberAddressesAbsent(t *testing.T) {
	dir := bundletest.Write(t, bundletest.Small())
	if _, err := os.Stat(filepath.Join(dir, "member_addresses.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the writer made member_addresses.jsonl with no row: %v", err)
	}
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Manifest().Counts["member_addresses"]; ok {
		t.Error("the manifest counts a file that the bundle does not hold")
	}
	if r.Has("member_addresses") || !r.Has("users") {
		t.Errorf("Has(member_addresses), Has(users) = %t, %t; want false, true", r.Has("member_addresses"), r.Has("users"))
	}
	n := 0
	if err := bundle.Each(r, func(int, bundle.MemberAddress) error { n++; return nil }); err != nil || n != 0 {
		t.Errorf("Each over the absent file = %d rows, %v", n, err)
	}
	if err := bundle.Check(r); err != nil {
		t.Errorf("Check = %v", err)
	}
}

// A bundle with member addresses holds the file and its count, and Check
// accepts it.
func TestMemberAddressesPresent(t *testing.T) {
	b := bundletest.Small()
	b.MemberAddresses = []bundle.MemberAddress{{UserID: 1, IP: "192.0.2.10"}, {UserID: 1, IP: "2001:db8::10"}, {UserID: 2, IP: "192.0.2.10"}}
	dir := bundletest.Write(t, b)
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Manifest().Counts["member_addresses"]; got != 3 {
		t.Errorf("counts.member_addresses = %d, want 3", got)
	}
	var got []bundle.MemberAddress
	if err := bundle.Each(r, func(_ int, a bundle.MemberAddress) error { got = append(got, a); return nil }); err != nil || len(got) != 3 {
		t.Fatalf("Each = %v, %v", got, err)
	}
	if err := bundle.Check(r); err != nil {
		t.Errorf("Check = %v", err)
	}
}

func TestMemberAddressesRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		rows []bundle.MemberAddress
		want string
	}{
		{"unknown user", []bundle.MemberAddress{{UserID: 99, IP: "192.0.2.1"}},
			"member_addresses.jsonl:1: user_id: no row in users.jsonl has id 99"},
		{"repeated pair", []bundle.MemberAddress{{UserID: 1, IP: "192.0.2.1"}, {UserID: 1, IP: "192.0.2.1"}},
			"member_addresses.jsonl:2: ip: the pair (user_id 1, ip 192.0.2.1) appears already, on line 1"},
		{"mapped form of the same address", []bundle.MemberAddress{{UserID: 1, IP: "192.0.2.1"}, {UserID: 1, IP: "::ffff:192.0.2.1"}},
			"member_addresses.jsonl:2: ip: the pair (user_id 1, ip 192.0.2.1) appears already, on line 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := bundletest.Small()
			b.MemberAddresses = c.rows
			err := checkBoard(t, b)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Check = %v, want %q", err, c.want)
			}
		})
	}
}

// The field checks refuse a row that is not an address, has a zone, or is the
// unspecified address, before it is written.
func TestMemberAddressFieldChecks(t *testing.T) {
	for _, c := range []struct {
		row  bundle.MemberAddress
		want string
	}{
		{bundle.MemberAddress{UserID: 0, IP: "192.0.2.1"}, "user_id: is 0; an ID is 1 or more"},
		{bundle.MemberAddress{UserID: 1, IP: "192.0.2.0/24"}, "ip: is not an IP address"},
		{bundle.MemberAddress{UserID: 1, IP: "fe80::1%eth0"}, "ip: must not have a zone"},
		{bundle.MemberAddress{UserID: 1, IP: "0.0.0.0"}, "ip: is the unspecified address"},
		{bundle.MemberAddress{UserID: 1, IP: "::"}, "ip: is the unspecified address"},
		{bundle.MemberAddress{UserID: 1, IP: "::ffff:0.0.0.0"}, "ip: is the unspecified address"},
	} {
		w, err := bundle.Create(filepath.Join(t.TempDir(), "b"), "example", bundletest.Small().Settings)
		if err != nil {
			t.Fatal(err)
		}
		err = bundle.Add(w, c.row)
		if err == nil || !strings.Contains(err.Error(), "member_addresses.jsonl:1: "+c.want) {
			t.Errorf("Add(%+v) = %v, want %q", c.row, err, c.want)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// The manifest counts the optional file exactly when the bundle holds it.
func TestMemberAddressesManifestMustAgree(t *testing.T) {
	withFile := func(t *testing.T) string {
		b := bundletest.Small()
		b.MemberAddresses = []bundle.MemberAddress{{UserID: 1, IP: "192.0.2.1"}}
		return bundletest.Write(t, b)
	}
	t.Run("file without a count", func(t *testing.T) {
		dir := withFile(t)
		editObject(t, dir, "manifest.json", func(m map[string]any) {
			delete(m["counts"].(map[string]any), "member_addresses")
		})
		_, err := bundle.Open(dir)
		if err == nil || !strings.Contains(err.Error(), `manifest.json: counts: has no key "member_addresses", and the bundle holds member_addresses.jsonl`) {
			t.Errorf("Open = %v", err)
		}
	})
	t.Run("count without a file", func(t *testing.T) {
		dir := withFile(t)
		if err := os.Remove(filepath.Join(dir, "member_addresses.jsonl")); err != nil {
			t.Fatal(err)
		}
		_, err := bundle.Open(dir)
		if err == nil || !strings.Contains(err.Error(), "member_addresses.jsonl: is missing, and manifest.json counts it") {
			t.Errorf("Open = %v", err)
		}
	})
	t.Run("wrong count", func(t *testing.T) {
		dir := withFile(t)
		editObject(t, dir, "manifest.json", func(m map[string]any) {
			m["counts"].(map[string]any)["member_addresses"] = json.Number("2")
		})
		r, err := bundle.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := bundle.Check(r); err == nil || !strings.Contains(err.Error(), "member_addresses.jsonl: 1 rows, manifest says 2") {
			t.Errorf("Check = %v", err)
		}
	})
	t.Run("negative count", func(t *testing.T) {
		dir := withFile(t)
		editObject(t, dir, "manifest.json", func(m map[string]any) {
			m["counts"].(map[string]any)["member_addresses"] = json.Number("-1")
		})
		if _, err := bundle.Open(dir); err == nil || !strings.Contains(err.Error(), "counts.member_addresses: is -1") {
			t.Errorf("Open = %v", err)
		}
	})
}
