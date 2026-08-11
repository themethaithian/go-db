package db_test

import (
	"testing"

	"github.com/themethaithian/go-db/internal/db"
)

func TestEngineValid(t *testing.T) {
	cases := []struct {
		name  string
		value db.Engine
		want  bool
	}{
		{"mysql", db.EngineMySQL, true},
		{"redis", db.EngineRedis, true},
		{"mongodb", db.EngineMongoDB, true},
		{"unset", db.Engine(""), false},
		{"unknown", db.Engine("postgres"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Valid(); got != tc.want {
				t.Errorf("Engine(%q).Valid() = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
