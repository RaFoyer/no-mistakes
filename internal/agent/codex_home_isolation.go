package agent

import "context"

// codexHomeIsolatedAgent marks non-Codex pipeline invocations so their
// subprocess environment drops CODEX_HOME without ever receiving the selected
// root value itself.
type codexHomeIsolatedAgent struct {
	Agent
}

func (a codexHomeIsolatedAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return a.Agent.Run(withCodexHomeIsolation(ctx), opts)
}

func (a codexHomeIsolatedAgent) SupportsSessionResume() bool {
	return SupportsSessionResume(a.Agent)
}

func (a codexHomeIsolatedAgent) SupportsSessionProvider(provider string) bool {
	return SupportsSessionProvider(a.Agent, provider)
}

func (a codexHomeIsolatedAgent) ReportsAgentAttempts() bool {
	return ReportsAgentAttempts(a.Agent)
}

func (a codexHomeIsolatedAgent) ReportsAgentRoutes() bool {
	return ReportsAgentRoutes(a.Agent)
}

func (a codexHomeIsolatedAgent) NeutralizesGateInstructions() bool {
	return NeutralizesGateInstructions(a.Agent)
}

// WithCodexHomeIsolation wraps a non-Codex pipeline adapter. It is idempotent
// and carries no state-root value.
func WithCodexHomeIsolation(a Agent) Agent {
	if a == nil {
		return nil
	}
	if _, ok := a.(codexHomeIsolatedAgent); ok {
		return a
	}
	return codexHomeIsolatedAgent{Agent: a}
}
