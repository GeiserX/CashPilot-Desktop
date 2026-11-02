package main

import (
	"context"

	"github.com/GeiserX/CashPilot-Desktop/internal/noderead"
)

// readNodes reads every running copy of slug from inside its container, the way its
// catalog entry's collector.node_read says (internal/noderead). It answers
// noderead.ErrNotDeclared for a service that declares no reader, so its collector
// keeps the account-API path.
func (a *App) readNodes(ctx context.Context, slug string) ([]noderead.Reading, error) {
	if a.catalog == nil || a.services == nil {
		return nil, noderead.ErrNotDeclared
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return nil, noderead.ErrNotDeclared
	}
	reader, ok := noderead.Compile(svc.Collector.NodeRead)
	if !ok {
		return nil, noderead.ErrNotDeclared
	}
	return noderead.ReadAll(ctx, a.services, slug, reader)
}
