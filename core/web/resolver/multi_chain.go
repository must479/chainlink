package resolver

import (
	"github.com/smartcontractkit/chainlink-terra/pkg/terra/db"
	"github.com/smartcontractkit/chainlink/core/chains/evm/types"
)

type NewMultiChainsPayloadParams struct {
	terraChains []db.Chain
	evmChains   []types.Chain
}

type MultiChainsPayloadResolver struct {
	terraChains []db.Chain
	evmChains   []types.Chain
	total       int32
}

func NewMultiChainsPayload(p NewMultiChainsPayloadParams, total int32) *MultiChainsPayloadResolver {
	return &MultiChainsPayloadResolver{
		terraChains: p.terraChains,
		evmChains:   p.evmChains,
		total:       total,
	}
}

// TODO: how do I create a resolver for a union type of MultiChainsPayload?

func (r *MultiChainsPayloadResolver) Metadata() *PaginationMetadataResolver {
	return NewPaginationMetadata(r.total)
}
