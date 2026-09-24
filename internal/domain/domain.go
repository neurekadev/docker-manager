// Package domain holds DockYard's shared domain types and use-case contracts.
//
// Domain types are free of HTTP, database and Docker concerns: no JSON/Bun
// struct tags, no huma, bun or moby imports. Transport DTOs live in
// internal/manager/api, database models in internal/manager/store, Docker SDK
// types behind the agent adapter (#21). Convert explicitly at the boundaries.
package domain

import "time"

// Instance describes this DockYard installation.
type Instance struct {
	ID        string
	CreatedAt time.Time
}
