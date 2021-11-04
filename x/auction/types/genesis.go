package types

import (
	"bytes"
	"fmt"

	types "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	proto "github.com/gogo/protobuf/proto"
)

// DefaultNextAuctionID is the starting point for auction IDs.
const DefaultNextAuctionID uint64 = 1

// GenesisAuction extends the auction interface to add functionality
// needed for initializing auctions from genesis.
type GenesisAuction interface {
	Auction
	GetModuleAccountCoins() sdk.Coins
	Validate() error
}

// PackGenesisAuctions converts a GenesisAuction slice to Any slice
func PackGenesisAuctions(ga []GenesisAuction) ([]*types.Any, error) {
	gaAny := make([]*types.Any, len(ga))
	for i, genesisAuction := range ga {
		msg, ok := genesisAuction.(proto.Message)
		if !ok {
			return nil, fmt.Errorf("cannot proto marshal %T", genesisAuction)
		}
		any, err := types.NewAnyWithValue(msg)
		if err != nil {
			return nil, err
		}
		gaAny[i] = any
	}

	return gaAny, nil
}

// UnpackGenesisAuctions converts Any slice to GenesisAuctions slice
func UnpackGenesisAuctions(genesisAuctionsAny []*types.Any) ([]GenesisAuction, error) {
	genesisAuctions := make([]GenesisAuction, len(genesisAuctionsAny))
	for i, any := range genesisAuctionsAny {
		genesisAuction, ok := any.GetCachedValue().(GenesisAuction)
		if !ok {
			return nil, fmt.Errorf("expected genesis auction")
		}
		genesisAuctions[i] = genesisAuction
	}

	return genesisAuctions, nil
}

// NewGenesisState returns a new genesis state object for auctions module.
func NewGenesisState(nextID uint64, ap Params, ga []GenesisAuction) *GenesisState {
	packedGA, err := PackGenesisAuctions(ga)
	if err != nil {
		panic(err)
	}
	return &GenesisState{
		NextAuctionId: nextID,
		Params:        ap,
		Auctions:      packedGA,
	}
}

// DefaultGenesisState returns the default genesis state for auction module.
func DefaultGenesisState() *GenesisState {
	return NewGenesisState(
		DefaultNextAuctionID,
		DefaultParams(),
		[]GenesisAuction{},
	)
}

// Equal checks whether two GenesisState structs are equivalent.
func (gs GenesisState) Equal(gs2 GenesisState) bool {
	b1 := ModuleCdc.Amino.MustMarshalBinaryBare(&gs)
	b2 := ModuleCdc.Amino.MustMarshalBinaryBare(&gs2)
	return bytes.Equal(b1, b2)
}

// IsEmpty returns true if a GenesisState is empty.
func (gs GenesisState) IsEmpty() bool {
	return gs.Equal(GenesisState{})
}

// Validate validates genesis inputs. It returns error if validation of any input fails.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return err
	}

	auctions, err := UnpackGenesisAuctions(gs.Auctions)
	if err != nil {
		return err
	}

	ids := map[uint64]bool{}
	for _, a := range auctions {

		if err := a.Validate(); err != nil {
			return fmt.Errorf("found invalid auction: %w", err)
		}

		if ids[a.GetId()] {
			return fmt.Errorf("found duplicate auction ID (%d)", a.GetId())
		}
		ids[a.GetId()] = true

		if a.GetId() >= gs.NextAuctionId {
			return fmt.Errorf("found auction ID ≥ the nextAuctionID (%d ≥ %d)", a.GetId(), gs.NextAuctionId)
		}
	}
	return nil
}
