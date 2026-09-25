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

// InstanceSettings are the editable instance settings (GET/PATCH
// /api/v1/settings). The sign-in policy, schedule defaults and maintenance
// defaults are separate revisioned resources; deployment configuration
// (public URL, trusted proxies, ...) comes from environment variables and is
// read-only.
type InstanceSettings struct {
	// Name is the display name of this DockYard (1-64 characters).
	Name      string
	Revision  int64
	UpdatedAt time.Time
}

// InstanceSettingsPatch changes instance settings (nil = unchanged).
type InstanceSettingsPatch struct {
	Name *string
}
