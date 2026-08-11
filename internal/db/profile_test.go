package db_test

import (
	"testing"

	"github.com/themethaithian/go-db/internal/db"
)

// These tests pin Profile.Address's per-Engine default port: a Profile that
// does not set one gets its Engine's usual port, so a Profile saved before
// v1's default was chosen still dials the right place.
func TestProfileAddressDefaultsPortPerEngine(t *testing.T) {
	cases := []struct {
		name   string
		engine db.Engine
		want   string
	}{
		{"unset engine defaults to MySQL's port", db.Engine(""), "db.internal:3306"},
		{"mysql", db.EngineMySQL, "db.internal:3306"},
		{"redis", db.EngineRedis, "db.internal:6379"},
		{"mongodb", db.EngineMongoDB, "db.internal:27017"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := db.Profile{Name: "p", Host: "db.internal", Engine: tc.engine}
			if got := p.Address(); got != tc.want {
				t.Errorf("Address() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProfileAddressHonoursAnExplicitPort(t *testing.T) {
	p := db.Profile{Name: "p", Host: "db.internal", Port: 7000, Engine: db.EngineRedis}
	if got, want := p.Address(), "db.internal:7000"; got != want {
		t.Errorf("Address() = %q, want %q", got, want)
	}
}
