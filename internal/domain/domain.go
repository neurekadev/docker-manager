// Package domain holds Docker Manager's shared domain types and use-case contracts.
//
// Domain types are free of HTTP, database and Docker concerns: no JSON/Bun
// struct tags, no huma, bun or moby imports. Transport DTOs live in
// internal/manager/api, database models in internal/manager/store, Docker SDK
// types behind the agent adapter (#21). Convert explicitly at the boundaries.
package domain

import "time"

// Instance describes this Docker Manager installation.
type Instance struct {
	ID        string
	CreatedAt time.Time
	// Generation is raised by one in every copy a manager hands to a new
	// server (docs/internal/architecture/manager-move.md); agents keep the
	// highest they have seen and refuse managers with a lower one.
	Generation int64
}

// InstanceSettings are the editable instance settings (GET/PATCH
// /api/v1/settings). The sign-in policy, schedule defaults and maintenance
// defaults are separate revisioned resources; deployment configuration
// (public URL, trusted proxies, ...) comes from environment variables and is
// read-only.
type InstanceSettings struct {
	// Name is the display name of this Docker Manager (1-64 characters).
	Name      string
	Revision  int64
	UpdatedAt time.Time
}

// InstanceSettingsPatch changes instance settings (nil = unchanged).
type InstanceSettingsPatch struct {
	Name *string
}
