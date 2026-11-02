package main

import (
	"context"
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/noderead"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// readNodes reads every running copy of slug, the way its catalog entry's
// collector.node_read says (internal/noderead). It answers noderead.ErrNotDeclared
// for a service that declares no reader, so its collector keeps the account-API path.
//
// A service Desktop runs as a native process has no container to run the command
// in, so its node is read in Go instead (noderead.NativeReaders), and the text goes
// through the same regexes.
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
	if a.store != nil {
		if dep, found, err := a.store.GetDeployment(slug); err == nil && found && dep.Runtime == runtime.NativeRuntimeKind {
			native, ok := noderead.NativeReaders[slug]
			if !ok {
				return nil, fmt.Errorf("CashPilot cannot read a native %s node", slug)
			}
			return noderead.ReadNative(ctx, native, reader), nil
		}
	}
	return noderead.ReadAll(ctx, a.services, slug, reader)
}
