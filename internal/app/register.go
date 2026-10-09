// Package app composes client-owned Go features.
package app

import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"

// Register calls each client feature's Register function. Keep database work in
// request handlers: route discovery and validation call this with reg.Pool nil.
func Register(reg *web.Registry) {}
