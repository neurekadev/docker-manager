package stacks

import (
	"context"
	"encoding/json"
)

// ImportLabelMetaForTest is importLabelMeta.
var ImportLabelMetaForTest = importLabelMeta

// ReconcileForTest runs the reconnect reconciliation synchronously with
// requests sent through a.
func (s *Service) ReconcileForTest(ctx context.Context, env string, a Agents) {
	s.reconcile(ctx, env, func(ctx context.Context, name string, in, out any) error {
		raw, err := a.RequestEnvironment(ctx, env, name, in, s.opts.RequestTimeout)
		if err != nil {
			return agentError(err)
		}
		return json.Unmarshal(raw, out)
	})
}
