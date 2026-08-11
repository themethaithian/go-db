package db

// Engine is the kind of database a Profile reaches. It is a first-class
// attribute of Profile (ADR-0006), not a detail buried in the Driver: per
// CONTEXT.md, the Engine determines the statement language the editor
// accepts, the classifier that judges it, and the shape of results.
// Everything else — the Approval Gate, both Origin policies, the Connection
// Registry, the audit log — is Engine-agnostic and does not branch on it.
//
// The zero value is not a fourth Engine; it means "unset" and is never read
// as one. ProfileStore normalizes it to EngineMySQL once, at load and at
// save, so every Profile any caller sees already names a concrete Engine.
type Engine string

const (
	EngineMySQL   Engine = "mysql"
	EngineRedis   Engine = "redis"
	EngineMongoDB Engine = "mongodb"
)

// Valid reports whether e is one of the three Engines go-db knows. The zero
// value is deliberately not valid: callers that mean "unset, default it" use
// ProfileStore's normalization rather than treating "" as a fourth Engine.
func (e Engine) Valid() bool {
	switch e {
	case EngineMySQL, EngineRedis, EngineMongoDB:
		return true
	default:
		return false
	}
}
