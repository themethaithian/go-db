package db

// tlsConfig is unexported, so its test lives inside the package — the way
// mongo_test.go and redis_test.go beside it do. The Profile tests that only
// need the exported surface stay in profile_test.go, which is package db_test.

import (
	"crypto/tls"
	"testing"
)

// TestProfileTLSConfig pins what a Profile's TLS field means once it reaches a
// Driver. Every adapter asks this one method, so what it answers is what "TLS"
// means on all three Engines rather than three near-identical settings.
func TestProfileTLSConfig(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		want    *tls.Config // nil means the Profile asked for plaintext
	}{
		{
			name:    "no TLS settings is a plaintext connection",
			profile: Profile{Name: "p", Host: "db.internal"},
			want:    nil,
		},
		{
			name:    "TLS on and verifying",
			profile: Profile{Name: "p", Host: "db.internal", TLS: &TLSSettings{}},
			want:    &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "db.internal"}, //nolint:gosec // the want, not a dial
		},
		{
			name:    "TLS on with verification waived",
			profile: Profile{Name: "p", Host: "db.internal", TLS: &TLSSettings{SkipVerify: true}},
			want:    &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "db.internal", InsecureSkipVerify: true}, //nolint:gosec // the want, not a dial
		},
		{
			// A certificate can name an IP in a SAN, and crypto/tls matches
			// one there; nothing has to turn the literal into a name first.
			name:    "an IP literal is the server name as it stands",
			profile: Profile{Name: "p", Host: "127.0.0.1", TLS: &TLSSettings{SkipVerify: true}},
			want:    &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "127.0.0.1", InsecureSkipVerify: true}, //nolint:gosec // the want, not a dial
		},
		{
			// Host is what the certificate names, and a Profile reached
			// through a tunnel is dialled at a forwarded local port that no
			// certificate could ever name. The port a Profile carries is not
			// part of the server name either way.
			name: "a tunnelled Profile still names its own host",
			profile: Profile{
				Name: "p", Host: "db.internal", Port: 7000,
				SSH: &SSHTunnel{Host: "bastion.internal", User: "jump"},
				TLS: &TLSSettings{},
			},
			want: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "db.internal"}, //nolint:gosec // the want, not a dial
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.profile.tlsConfig()

			if tc.want == nil {
				if got != nil {
					t.Fatalf("tlsConfig() = %#v, want nil — the Profile asked for no TLS", got)
				}
				return
			}
			if got == nil {
				t.Fatal("tlsConfig() = nil, want a config — the Profile asked for TLS")
			}
			if got.MinVersion != tc.want.MinVersion {
				t.Errorf("MinVersion = %#x, want %#x", got.MinVersion, tc.want.MinVersion)
			}
			if got.ServerName != tc.want.ServerName {
				t.Errorf("ServerName = %q, want %q", got.ServerName, tc.want.ServerName)
			}
			if got.InsecureSkipVerify != tc.want.InsecureSkipVerify {
				t.Errorf("InsecureSkipVerify = %v, want %v", got.InsecureSkipVerify, tc.want.InsecureSkipVerify)
			}
		})
	}
}

// TestProfileTLSConfigIsFreshEachCall pins that no two connections share one
// config value. The drivers hand it to client libraries that keep it, and one
// of them mutating a shared config would change every other Profile's idea of
// what it is talking to.
func TestProfileTLSConfigIsFreshEachCall(t *testing.T) {
	profile := Profile{Name: "p", Host: "db.internal", TLS: &TLSSettings{}}

	first, second := profile.tlsConfig(), profile.tlsConfig()
	if first == second {
		t.Error("tlsConfig() returned the same config twice, want a fresh one each call")
	}
}
