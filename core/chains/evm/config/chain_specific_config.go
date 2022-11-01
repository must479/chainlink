package config

import (
	"github.com/smartcontractkit/chainlink/core/assets"
	"github.com/smartcontractkit/chainlink/core/utils"
)

// TODO move to evmtest?
var (
	DefaultGasLimit               uint32 = 500000
	DefaultMinimumContractPayment        = assets.NewLinkFromJuels(10_000_000_000_000) // 0.00001 LINK
	MaxLegalGasPrice                     = assets.NewWei(utils.MaxUint256)
)
