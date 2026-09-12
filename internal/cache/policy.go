package cache

import (
	"fmt"
	"strings"
)

type Policy string

const (
	// PolicyInherit is only used by per-instance overrides. It is not a
	// user-selectable cache policy.
	PolicyInherit Policy = ""
	PolicyAuto    Policy = "auto"
	PolicyMemory  Policy = "memory"
	PolicyDisk    Policy = "disk"
)

func ParsePolicy(value string) (Policy, error) {
	policy := Policy(strings.ToLower(strings.TrimSpace(value)))
	if policy == PolicyInherit {
		return PolicyAuto, nil
	}
	if !policy.IsConcrete() {
		return PolicyInherit, fmt.Errorf("invalid cache policy %q: expected auto, memory, or disk", value)
	}
	return policy, nil
}

func ResolvePolicy(override, fallback Policy) (Policy, error) {
	policy := override
	if policy == PolicyInherit {
		policy = fallback
	}
	if !policy.IsConcrete() {
		return PolicyInherit, fmt.Errorf("invalid cache policy %q: expected auto, memory, or disk", policy)
	}
	return policy, nil
}

func (p Policy) IsConcrete() bool {
	return p == PolicyAuto || p == PolicyMemory || p == PolicyDisk
}

func (p Policy) MarshalText() ([]byte, error) {
	if p == PolicyInherit {
		return []byte{}, nil
	}
	if !p.IsConcrete() {
		return nil, fmt.Errorf("invalid cache policy %q", p)
	}
	return []byte(p), nil
}

func (p *Policy) UnmarshalText(text []byte) error {
	policy, err := ParsePolicy(string(text))
	if err != nil {
		return err
	}
	*p = policy
	return nil
}
